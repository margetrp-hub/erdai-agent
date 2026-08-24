package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const qqOfficialWebhookTransport = "qq_official_webhook"

const (
	qqWebhookSignatureHeader = "X-Signature-Ed25519"
	qqWebhookTimestampHeader = "X-Signature-Timestamp"
	qqWebhookLegacyPath      = "/astrbot-qo-webhook/callback"
)

type qqOfficialWebhookConnector struct {
	qq          *qqOfficialConnector
	webhookUUID string
	host        string
	port        int
	cancel      context.CancelFunc
	server      *platformWebhookServer
	dedupeMu    sync.Mutex
	dedupe      map[string]time.Time
}

type qqOfficialWebhookEnvelope struct {
	ID     string          `json:"id"`
	OpCode int             `json:"op"`
	Type   string          `json:"t"`
	Data   json.RawMessage `json:"d"`
}

type qqOfficialWebhookChallenge struct {
	EventTimestamp string `json:"event_ts"`
	PlainToken     string `json:"plain_token"`
}

func newQQOfficialWebhookConnector(runtime *AgentRuntime, platform mgmtPlatform) (*qqOfficialWebhookConnector, error) {
	qq, err := newQQOfficialConnector(runtime, platform)
	if err != nil {
		return nil, err
	}
	qq.transport = qqOfficialWebhookTransport
	host, _ := platform.Settings["callback_server_host"].(string)
	if strings.TrimSpace(host) == "" {
		host = "0.0.0.0"
	}
	return &qqOfficialWebhookConnector{
		qq: qq, webhookUUID: resolvePlatformCredential(platform, "webhook_uuid"),
		host: host, port: kookIntSetting(platform, "port", 6196), dedupe: map[string]time.Time{},
	}, nil
}

func (c *qqOfficialWebhookConnector) ID() string   { return c.qq.ID() }
func (c *qqOfficialWebhookConnector) Type() string { return qqOfficialWebhookTransport }

func (c *qqOfficialWebhookConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	if err := c.qq.initializeAPI(ctx); err != nil {
		cancel()
		c.qq.setError(err)
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc(c.webhookPath(), c.handleWebhook)
	server, err := startPlatformWebhookServer(ctx, c.host, c.port, mux)
	if err != nil {
		cancel()
		c.qq.setError(err)
		return err
	}
	c.server = server
	c.qq.setStatus("connected")
	return nil
}

func (c *qqOfficialWebhookConnector) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	err := c.server.Close()
	c.qq.setStatus("stopped")
	return err
}

func (c *qqOfficialWebhookConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	return c.qq.Deliver(ctx, route, delivery)
}

func (c *qqOfficialWebhookConnector) Health() platformConnectorHealth { return c.qq.Health() }

func (c *qqOfficialWebhookConnector) webhookPath() string {
	if c.webhookUUID == "" {
		return qqWebhookLegacyPath
	}
	return "/webhooks/" + url.PathEscape(c.webhookUUID)
}

func (c *qqOfficialWebhookConnector) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var envelope qqOfficialWebhookEnvelope
	if json.Unmarshal(body, &envelope) != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if envelope.OpCode == 13 {
		c.handleChallenge(w, envelope.Data)
		return
	}
	if !verifyQQWebhookSignature(c.qq.secret, r.Header.Get(qqWebhookTimestampHeader), r.Header.Get(qqWebhookSignatureHeader), body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	if envelope.OpCode == 0 && envelope.Type != "" {
		key, duplicate := c.claimEvent(envelope.ID)
		if !duplicate {
			if err = c.handleEvent(r.Context(), envelope); err != nil {
				c.releaseEvent(key)
				http.Error(w, "event rejected", http.StatusInternalServerError)
				return
			}
		}
	}
	writeQQWebhookJSON(w, http.StatusOK, map[string]any{"opcode": 12})
}

func (c *qqOfficialWebhookConnector) handleChallenge(w http.ResponseWriter, raw json.RawMessage) {
	var challenge qqOfficialWebhookChallenge
	if json.Unmarshal(raw, &challenge) != nil || challenge.PlainToken == "" {
		http.Error(w, "invalid challenge", http.StatusBadRequest)
		return
	}
	privateKey, err := qqWebhookPrivateKey(c.qq.secret)
	if err != nil {
		http.Error(w, "invalid secret", http.StatusInternalServerError)
		return
	}
	signature := ed25519.Sign(privateKey, []byte(challenge.EventTimestamp+challenge.PlainToken))
	writeQQWebhookJSON(w, http.StatusOK, map[string]any{
		"plain_token": challenge.PlainToken,
		"signature":   hex.EncodeToString(signature),
	})
}

func (c *qqOfficialWebhookConnector) handleEvent(ctx context.Context, envelope qqOfficialWebhookEnvelope) error {
	kind := map[string]string{
		"GROUP_AT_MESSAGE_CREATE": "group",
		"GROUP_MESSAGE_CREATE":    "group_plain",
		"AT_MESSAGE_CREATE":       "channel",
		"DIRECT_MESSAGE_CREATE":   "guild_dm",
		"C2C_MESSAGE_CREATE":      "c2c",
	}[strings.ToUpper(strings.TrimSpace(envelope.Type))]
	if kind == "" || (kind == "guild_dm" && !c.qq.guildDM) {
		return nil
	}
	var message qqOfficialRawMessage
	if err := json.Unmarshal(envelope.Data, &message); err != nil {
		return err
	}
	// Mirror the gateway path: without these two passes a webhook inbound never
	// gets ReplyToBot or raw-content mention recognition, so IsWake is wrong
	// for group_plain messages and failure-notice targeting misfires.
	inbound := normalizeQQOfficialRawMessage(message, kind)
	c.qq.recognizeRawBotMention(&inbound, message)
	c.qq.recognizeReplyToBot(&inbound)
	return c.qq.handleNormalizedInbound(ctx, nil, inbound, kind)
}

func (c *qqOfficialWebhookConnector) claimEvent(eventID string) (string, bool) {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return "", false
	}
	now := time.Now()
	c.dedupeMu.Lock()
	defer c.dedupeMu.Unlock()
	for key, expiresAt := range c.dedupe {
		if !now.Before(expiresAt) {
			delete(c.dedupe, key)
		}
	}
	if expiresAt, found := c.dedupe[eventID]; found && now.Before(expiresAt) {
		return eventID, true
	}
	c.dedupe[eventID] = now.Add(qqOfficialInboundDedupeTTL)
	return eventID, false
}

func (c *qqOfficialWebhookConnector) releaseEvent(eventID string) {
	if eventID == "" {
		return
	}
	c.dedupeMu.Lock()
	delete(c.dedupe, eventID)
	c.dedupeMu.Unlock()
}

func qqWebhookPrivateKey(secret string) (ed25519.PrivateKey, error) {
	if secret == "" {
		return nil, errors.New("QQ official bot secret is empty")
	}
	seed := []byte(secret)
	for len(seed) < ed25519.SeedSize {
		seed = append(seed, seed...)
	}
	return ed25519.NewKeyFromSeed(seed[:ed25519.SeedSize]), nil
}

func verifyQQWebhookSignature(secret, timestamp, signatureHex string, body []byte) bool {
	if timestamp == "" || signatureHex == "" {
		return false
	}
	signature, err := hex.DecodeString(signatureHex)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return false
	}
	privateKey, err := qqWebhookPrivateKey(secret)
	if err != nil {
		return false
	}
	return ed25519.Verify(privateKey.Public().(ed25519.PublicKey), append([]byte(timestamp), body...), signature)
}

func writeQQWebhookJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

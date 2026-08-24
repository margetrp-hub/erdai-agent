package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type platformConnectorHealth struct {
	ID             string         `json:"id"`
	Type           string         `json:"type"`
	Status         string         `json:"status"`
	LastEventAt    *string        `json:"lastEventAt"`
	LastDeliveryAt *string        `json:"lastDeliveryAt"`
	LastError      string         `json:"lastError,omitempty"`
	Details        map[string]any `json:"details,omitempty"`
}

type platformReplyRoute struct {
	ConnectorID string `json:"connectorId"`
	Transport   string `json:"transport"`
	Kind        string `json:"kind"`
	TargetID    string `json:"targetId"`
	TargetType  string `json:"targetType,omitempty"`
	AccessHash  int64  `json:"accessHash,omitempty"`
	GuildID     string `json:"guildId,omitempty"`
	ChannelID   string `json:"channelId,omitempty"`
	MessageID   string `json:"messageId"`
}

type platformConnector interface {
	ID() string
	Type() string
	Start(context.Context) error
	Close() error
	Deliver(context.Context, platformReplyRoute, leasedTransportDelivery) error
	Health() platformConnectorHealth
}

type platformDeliveryError struct {
	Retryable bool
	Reason    string
	Cause     error
}

func (e *platformDeliveryError) Error() string {
	if e.Cause != nil {
		return e.Reason + ": " + e.Cause.Error()
	}
	return e.Reason
}

var connectorErrorSecretPatterns = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)/bot[^/\s"']+/`), "/bot[redacted]/"},
	{regexp.MustCompile(`(?i)(access[_-]?token|token|secret|api[_-]?key)=([^&\s"']+)`), "$1=[redacted]"},
	{regexp.MustCompile(`(?i)(authorization[:=]\s*bearer\s+)[^\s,"']+`), "$1[redacted]"},
}

func redactConnectorError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	for _, item := range connectorErrorSecretPatterns {
		message = item.pattern.ReplaceAllString(message, item.replacement)
	}
	if runes := []rune(message); len(runes) > 500 {
		message = string(runes[:500])
	}
	return message
}

type platformConnectorManager struct {
	runtime    *AgentRuntime
	connectors map[string]platformConnector
	configured []platformConnectorHealth
	poll       time.Duration
	cancel     context.CancelFunc
	wg         sync.WaitGroup
}

func newPlatformConnectorManager(runtime *AgentRuntime) (*platformConnectorManager, error) {
	platforms, err := runtime.configStore.mgmtPlatforms()
	if err != nil {
		return nil, err
	}
	manager := &platformConnectorManager{
		runtime: runtime, connectors: map[string]platformConnector{}, poll: time.Second,
	}
	if runtime.realtime != nil {
		manager.connectors[runtime.realtime.ID()] = runtime.realtime
	}
	var channelPolicy struct {
		DeliveryPollSeconds float64 `json:"deliveryPollSeconds"`
	}
	if raw, rawErr := runtime.configStore.integrationRaw("channel_runtime"); rawErr == nil {
		_ = json.Unmarshal(raw, &channelPolicy)
	}
	if channelPolicy.DeliveryPollSeconds >= 0.2 && channelPolicy.DeliveryPollSeconds <= 30 {
		manager.poll = time.Duration(channelPolicy.DeliveryPollSeconds * float64(time.Second))
	}
	for _, platform := range platforms {
		if !platform.Enabled {
			continue
		}
		status := platformConnectorHealth{ID: platform.ID, Type: platform.Type, Status: "error"}
		connector, connectorErr := newPlatformConnector(runtime, platform)
		if connectorErr != nil {
			status.Status = "error"
			status.LastError = redactConnectorError(connectorErr)
			manager.configured = append(manager.configured, status)
			continue
		}
		manager.connectors[connector.ID()] = connector
	}
	return manager, nil
}

func (m *platformConnectorManager) Start(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel
	for id, connector := range m.connectors {
		if err := connector.Start(ctx); err != nil {
			m.configured = append(m.configured, platformConnectorHealth{
				ID: id, Type: connector.Type(), Status: "error", LastError: redactConnectorError(err),
			})
			delete(m.connectors, id)
		}
	}
	if len(m.connectors) == 0 {
		return
	}
	m.wg.Add(1)
	go m.deliveryLoop(ctx)
}

func (m *platformConnectorManager) Close() error {
	if m == nil {
		return nil
	}
	if m.cancel != nil {
		m.cancel()
	}
	var closeErr error
	for _, connector := range m.connectors {
		closeErr = errors.Join(closeErr, connector.Close())
	}
	m.wg.Wait()
	return closeErr
}

func (m *platformConnectorManager) deliveryLoop(ctx context.Context) {
	defer m.wg.Done()
	ticker := time.NewTicker(m.poll)
	defer ticker.Stop()
	for {
		m.deliverPending(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *platformConnectorManager) deliverPending(ctx context.Context) {
	deliveries, err := m.runtime.leaseTransportDeliveries(ctx, "erdai-go-platforms", 10, 30)
	if err != nil {
		return
	}
	for _, delivery := range deliveries {
		if m.runtime.platformDeliveryWasSent(ctx, delivery.ID) {
			_ = m.runtime.ackTransportDelivery(ctx, delivery.ID)
			continue
		}
		route, routeErr := m.runtime.platformRoute(ctx, delivery.ReplyHandle)
		connector := m.connectors[route.ConnectorID]
		if routeErr == nil && connector == nil {
			routeErr = errors.New("connector is not running")
		}
		if routeErr != nil {
			_, _ = m.runtime.failTransportDelivery(ctx, delivery.ID, false, "unknown_reply_handle")
			continue
		}
		sendStarted := time.Now()
		err = connector.Deliver(ctx, route, delivery)
		_ = m.runtime.recordRunStage(delivery.RunID, "connector_send", sendStarted, map[string]any{
			"connectorId": route.ConnectorID, "deliveryId": delivery.ID, "error": err != nil,
		})
		if err != nil {
			retryable, reason := true, "platform_delivery_failed"
			var deliveryErr *platformDeliveryError
			if errors.As(err, &deliveryErr) {
				retryable = deliveryErr.Retryable
				if strings.TrimSpace(deliveryErr.Reason) != "" {
					reason = deliveryErr.Reason
				}
			}
			_, _ = m.runtime.failTransportDelivery(ctx, delivery.ID, retryable, reason)
			continue
		}
		if err = m.runtime.markPlatformDeliverySent(ctx, delivery.ID); err != nil {
			_, _ = m.runtime.failTransportDelivery(ctx, delivery.ID, true, "delivery_receipt_failed")
			continue
		}
		_ = m.runtime.ackTransportDelivery(ctx, delivery.ID)
	}
}

func (m *platformConnectorManager) Health() []platformConnectorHealth {
	if m == nil {
		return []platformConnectorHealth{}
	}
	values := append([]platformConnectorHealth{}, m.configured...)
	for _, connector := range m.connectors {
		values = append(values, connector.Health())
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values
}

func (m *platformConnectorManager) WeixinOCQRCode(id string) (string, bool) {
	if m == nil {
		return "", false
	}
	connector, ok := m.connectors[id].(*weixinOCConnector)
	if !ok {
		return "", false
	}
	value := connector.QRCodeValue()
	return value, value != ""
}

func (m *platformConnectorManager) TelegramUser(id string) (*telegramUserConnector, bool) {
	if m == nil {
		return nil, false
	}
	connector, ok := m.connectors[id].(*telegramUserConnector)
	return connector, ok
}

func resolvePlatformCredential(platform mgmtPlatform, name string) string {
	reference, _ := platform.CredentialRefs[name].(string)
	if reference == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(reference))
}

func (a *AgentRuntime) platformPseudonym(kind, value string) string {
	mac := hmac.New(sha256.New, a.identitySecret)
	_, _ = mac.Write([]byte(kind + ":" + value))
	return kind + "_" + hex.EncodeToString(mac.Sum(nil))[:32]
}

func (a *AgentRuntime) rememberPlatformRoute(ctx context.Context, eventID string, route platformReplyRoute) (string, error) {
	if eventID == "" || route.ConnectorID == "" || route.Transport == "" ||
		route.Kind == "" || route.TargetID == "" {
		return "", errors.New("platform route is incomplete")
	}
	handle := a.platformPseudonym("reply", eventID)
	encoded, err := json.Marshal(route)
	if err != nil {
		return "", err
	}
	ciphertext, err := a.encrypt(encoded)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = a.db.ExecContext(ctx, `
		INSERT INTO platform_reply_routes (
			reply_handle, event_id, route_cipher, next_sequence, created_at, updated_at
		) VALUES (?, ?, ?, 0, ?, ?)
		ON CONFLICT(event_id) DO NOTHING
	`, handle, eventID, ciphertext, now, now)
	if err != nil {
		return "", err
	}
	if err = a.db.QueryRowContext(ctx,
		"SELECT reply_handle FROM platform_reply_routes WHERE event_id = ?", eventID,
	).Scan(&handle); err != nil {
		return "", err
	}
	return handle, nil
}

func (a *AgentRuntime) forgetPlatformRoute(ctx context.Context, eventID string) {
	_, _ = a.db.ExecContext(ctx, `
		DELETE FROM platform_reply_routes WHERE event_id = ?
		AND NOT EXISTS (SELECT 1 FROM agent_runs WHERE event_id = ?)
	`, eventID, eventID)
}

func (a *AgentRuntime) platformRoute(ctx context.Context, handle string) (platformReplyRoute, error) {
	var ciphertext []byte
	if err := a.db.QueryRowContext(ctx,
		"SELECT route_cipher FROM platform_reply_routes WHERE reply_handle = ?", handle,
	).Scan(&ciphertext); err != nil {
		return platformReplyRoute{}, err
	}
	plaintext, err := a.decrypt(ciphertext)
	if err != nil {
		return platformReplyRoute{}, err
	}
	var route platformReplyRoute
	if err = json.Unmarshal(plaintext, &route); err != nil {
		return platformReplyRoute{}, err
	}
	return route, nil
}

func (a *AgentRuntime) nextPlatformMessageSequence(ctx context.Context, handle string) (uint32, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var current uint32
	if err = tx.QueryRowContext(ctx,
		"SELECT next_sequence FROM platform_reply_routes WHERE reply_handle = ?", handle,
	).Scan(&current); err != nil {
		return 0, err
	}
	current++
	if _, err = tx.ExecContext(ctx, `
		UPDATE platform_reply_routes SET next_sequence = ?, updated_at = ? WHERE reply_handle = ?
	`, current, time.Now().UTC().Format(time.RFC3339Nano), handle); err != nil {
		return 0, err
	}
	return current, tx.Commit()
}

func (a *AgentRuntime) platformDeliveryWasSent(ctx context.Context, deliveryID string) bool {
	var found int
	return a.db.QueryRowContext(ctx,
		"SELECT 1 FROM platform_sent_deliveries WHERE delivery_id = ?", deliveryID,
	).Scan(&found) == nil
}

func (a *AgentRuntime) markPlatformDeliverySent(ctx context.Context, deliveryID string) error {
	_, err := a.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO platform_sent_deliveries (delivery_id, sent_at) VALUES (?, ?)
	`, deliveryID, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func platformConnectorStartupError(platform mgmtPlatform, field string) error {
	return fmt.Errorf("%s %s is not configured", platform.ID, field)
}

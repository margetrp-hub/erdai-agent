package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type nativePlatformInbound struct {
	ConnectorID       string
	Transport         string
	MessageID         string
	RouteKind         string
	TargetID          string
	TargetType        string
	AccessHash        int64
	GuildID           string
	ChannelID         string
	ConversationID    string
	ConversationKind  string
	ThreadID          string
	SenderID          string
	SenderName        string
	Text              string
	ReplyToMessageID  string
	ReplyToSenderID   string
	ReplyToSenderName string
	ReplyToText       string
	Mentions          []transportMention
	Attachments       []transportAttachment
	OccurredAt        time.Time
	IsWake            bool
	IsAdmin           bool
	IsMentionBot      bool
	IsAtOthers        bool
	IsCommand         bool
	IsAtAll           bool
}

func (a *AgentRuntime) acceptNativePlatformInbound(ctx context.Context, inbound nativePlatformInbound) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"connector":    inbound.ConnectorID,
		"transport":    inbound.Transport,
		"message":      inbound.MessageID,
		"route kind":   inbound.RouteKind,
		"target":       inbound.TargetID,
		"conversation": inbound.ConversationID,
		"sender":       inbound.SenderID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("platform inbound %s is empty", name)
		}
	}
	if inbound.ConversationKind == "" {
		inbound.ConversationKind = "group"
	}
	if inbound.OccurredAt.IsZero() {
		inbound.OccurredAt = time.Now().UTC()
	}
	if len(inbound.Attachments) > 3 {
		inbound.Attachments = inbound.Attachments[:3]
	}
	if inbound.ConversationKind == "group" {
		conversationRef := a.platformPseudonym("conversation", inbound.ConversationID)
		decision, moderationErr := a.evaluateOwnedGroupModeration(ctx, inbound.ConnectorID, inbound.Transport, conversationRef, inbound.ConversationID, inbound.SenderID, inbound.SenderName, inbound.Text, inbound.IsAdmin)
		if moderationErr != nil {
			return moderationErr
		}
		if decision.Matched {
			a.recordGroupModerationAudit(decision, "group_moderation_detected", inbound.ConnectorID, inbound.ConversationID, inbound.MessageID, "")
			if decision.Mode == "enforce" {
				a.recordGroupModerationAudit(decision, "group_moderation_filtered", inbound.ConnectorID, inbound.ConversationID, inbound.MessageID, "")
				return nil
			}
		}
	}
	for index := range inbound.Attachments {
		inbound.Attachments[index].ID = fmt.Sprintf("attachment-%d", index+1)
	}
	eventID := a.platformPseudonym("event", inbound.ConnectorID+":"+inbound.MessageID)
	replyHandle, err := a.rememberPlatformRoute(ctx, eventID, platformReplyRoute{
		ConnectorID: inbound.ConnectorID,
		Transport:   inbound.Transport,
		Kind:        inbound.RouteKind,
		TargetID:    inbound.TargetID,
		TargetType:  inbound.TargetType,
		AccessHash:  inbound.AccessHash,
		GuildID:     inbound.GuildID,
		ChannelID:   inbound.ChannelID,
		MessageID:   inbound.MessageID,
	})
	if err != nil {
		return err
	}
	event := transportEvent{
		SchemaVersion:     2,
		EventID:           eventID,
		Transport:         inbound.Transport,
		TransportInstance: strings.TrimSpace(inbound.ConnectorID),
		ReplyHandle:       replyHandle,
		OccurredAt:        inbound.OccurredAt.UTC().Format(time.RFC3339Nano),
	}
	event.Conversation.Key = a.platformPseudonym("conversation", inbound.ConversationID)
	event.Conversation.Kind = inbound.ConversationKind
	event.Conversation.ThreadKey = strings.TrimSpace(inbound.ThreadID)
	event.Sender.Key = a.platformPseudonym("sender", inbound.SenderID)
	event.Sender.DisplayName = truncateRunes(inbound.SenderName, 80)
	event.Message.Text = strings.TrimSpace(inbound.Text)
	event.Message.ID = strings.TrimSpace(inbound.MessageID)
	event.Message.Attachments = inbound.Attachments
	event.Message.Mentions = inbound.Mentions
	if strings.TrimSpace(inbound.ReplyToMessageID) != "" {
		replySenderKey := ""
		if strings.TrimSpace(inbound.ReplyToSenderID) != "" {
			replySenderKey = a.platformPseudonym("sender", inbound.ReplyToSenderID)
		}
		event.Message.ReplyTo = &transportReplyReference{
			MessageID:         strings.TrimSpace(inbound.ReplyToMessageID),
			SenderKey:         replySenderKey,
			SenderDisplayName: truncateRunes(strings.TrimSpace(inbound.ReplyToSenderName), 80),
			Text:              truncateRunes(strings.TrimSpace(inbound.ReplyToText), 1000),
		}
	}
	event.Flags.IsWake = inbound.IsWake
	event.Flags.IsAdmin = inbound.IsAdmin
	event.Flags.IsMentionBot = inbound.IsMentionBot
	event.Flags.IsAtOthers = inbound.IsAtOthers
	event.Flags.IsCommand = inbound.IsCommand
	event.Flags.IsAtAll = inbound.IsAtAll
	event.Privacy.Transient = []string{"sender.displayName", "message.text", "message.attachments[].sourceUrl"}
	decision, err := a.acceptTrustedTransportEvent(ctx, event, eventID)
	if err != nil {
		return err
	}
	if disposition, _ := decision["disposition"].(string); disposition != "owned" {
		a.forgetPlatformRoute(ctx, eventID)
	}
	return nil
}

type nativeConnectorState struct {
	mu     sync.RWMutex
	health platformConnectorHealth
}

func newNativeConnectorState(platform mgmtPlatform) nativeConnectorState {
	return nativeConnectorState{health: platformConnectorHealth{
		ID: platform.ID, Type: platform.Type, Status: "starting",
	}}
}

func (s *nativeConnectorState) snapshot() platformConnectorHealth {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.health
}

func (s *nativeConnectorState) setStatus(status string) {
	s.mu.Lock()
	s.health.Status = status
	if status != "error" {
		s.health.LastError = ""
	}
	s.mu.Unlock()
}

func (s *nativeConnectorState) setDetails(details map[string]any) {
	s.mu.Lock()
	if len(details) == 0 {
		s.health.Details = nil
	} else {
		s.health.Details = details
	}
	s.mu.Unlock()
}

func (s *nativeConnectorState) setError(err error) {
	s.mu.Lock()
	s.health.Status = "error"
	if err != nil {
		s.health.LastError = redactConnectorError(err)
	}
	s.mu.Unlock()
}

func (s *nativeConnectorState) markEvent() {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	s.health.LastEventAt = &now
	if s.health.Status != "error" {
		s.health.Status = "connected"
	}
	s.mu.Unlock()
}

func (s *nativeConnectorState) markDelivery() {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s.mu.Lock()
	s.health.LastDeliveryAt = &now
	s.health.Status = "connected"
	s.health.LastError = ""
	s.mu.Unlock()
}

func nativePlatformAdminIDs(platform mgmtPlatform) map[string]struct{} {
	values := map[string]struct{}{}
	raw, _ := platform.Settings["admin_ids"].(string)
	envName := "ERDAI_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(platform.Type)) + "_ADMIN_IDS"
	if fromEnv := strings.TrimSpace(os.Getenv(envName)); fromEnv != "" {
		if raw != "" {
			raw += ","
		}
		raw += fromEnv
	}
	for _, value := range strings.FieldsFunc(raw, func(value rune) bool {
		return value == ',' || value == ';' || value == '\n' || value == '\r'
	}) {
		if value = strings.TrimSpace(value); value != "" {
			values[value] = struct{}{}
		}
	}
	return values
}

func nativePlatformIsAdmin(adminIDs map[string]struct{}, senderID string) bool {
	_, found := adminIDs[strings.TrimSpace(senderID)]
	return found
}

func nativePlatformIsCommand(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "/") && !strings.ContainsAny(text, "\r\n") && len([]rune(text)) <= 120
}

func nativeAttachmentOnlyPrompt(attachments []transportAttachment) string {
	for _, attachment := range attachments {
		if attachment.Kind == "image" {
			return "请看我发的图片，结合当前对话回应。"
		}
	}
	if len(attachments) > 0 {
		return "请查看我发的附件，结合当前对话回应。"
	}
	return ""
}

func readNativeMedia(attachment agentAttachment) ([]byte, string, error) {
	cleanPath := filepath.Clean(strings.TrimSpace(attachment.LocalPath))
	if !strings.HasPrefix(cleanPath, mediaMountRoot+string(os.PathSeparator)) {
		return nil, "", errors.New("attachment path is outside the media directory")
	}
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 || len(data) > maxImageBytes {
		return nil, "", errors.New("attachment size is invalid")
	}
	return data, cleanPath, nil
}

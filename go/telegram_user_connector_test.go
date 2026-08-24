package main

import (
	"path/filepath"
	"testing"
)

func TestTelegramUserConnectorUsesInstanceSessionAndCredentials(t *testing.T) {
	data := t.TempDir()
	t.Setenv("ERDAI_TELEGRAM_USER_TEST_HASH", "test-api-hash")
	runtime := &AgentRuntime{mediaDir: filepath.Join(data, "media")}
	platform := mgmtPlatform{
		ID: "telegram-user-one", Type: telegramUserTransport,
		Settings: map[string]any{
			"telegram_user_api_id":      float64(123456),
			"telegram_user_session_dir": filepath.Join(data, "telegram-sessions"),
		},
		CredentialRefs: map[string]any{"api_hash": "ERDAI_TELEGRAM_USER_TEST_HASH"},
	}
	connector, err := newTelegramUserConnector(runtime, platform)
	if err != nil {
		t.Fatal(err)
	}
	if connector.ID() != platform.ID || connector.Type() != telegramUserTransport {
		t.Fatalf("connector identity = %s/%s", connector.ID(), connector.Type())
	}
	want := filepath.Join(data, "telegram-sessions", platform.ID+".json")
	if connector.session != want {
		t.Fatalf("session path = %q, want %q", connector.session, want)
	}
}

func TestTelegramUserSessionCannotEscapeRuntimeData(t *testing.T) {
	data := t.TempDir()
	runtime := &AgentRuntime{mediaDir: filepath.Join(data, "media")}
	platform := mgmtPlatform{ID: "telegram-user-one", Settings: map[string]any{
		"telegram_user_session_dir": filepath.Join(data, "..", "outside"),
	}}
	if _, err := telegramUserSessionPath(runtime, platform); err == nil {
		t.Fatal("session path escaped runtime data")
	}
}

func TestTelegramUserRoutePeerRestoresAfterRestart(t *testing.T) {
	peer, err := telegramUserRoutePeer(platformReplyRoute{
		TargetID: "channel:1234", TargetType: "channel", AccessHash: 5678,
	})
	if err != nil {
		t.Fatal(err)
	}
	if peer != -1000000001234 {
		t.Fatalf("restored peer = %d", peer)
	}
	if got := telegramUserRouteMessageID("channel:1234:77"); got != 77 {
		t.Fatalf("reply message id = %d", got)
	}
}

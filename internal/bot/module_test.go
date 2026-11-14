package bot_test

import (
	"strings"
	"testing"

	"github.com/android-sms-gateway/telegram-bridge/internal/bot"
	botconfig "github.com/android-sms-gateway/telegram-bridge/internal/bot/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// TestWarnEmptyAllowlist pins the startup report the documentation promises:
// an empty allowlist fails closed, so the operator gets exactly one warning
// that names the environment key to set, and a configured allowlist stays
// quiet.
func TestWarnEmptyAllowlist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		allowed   []int64
		wantWarns int
	}{
		{name: "nil allowlist warns", allowed: nil, wantWarns: 1},
		{name: "empty allowlist warns", allowed: []int64{}, wantWarns: 1},
		{name: "configured allowlist stays quiet", allowed: []int64{123456789}, wantWarns: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			core, logs := observer.New(zap.WarnLevel)
			bot.WarnEmptyAllowlist(botconfig.Config{AllowedUserIDs: tt.allowed}, zap.New(core))

			if got := logs.Len(); got != tt.wantWarns {
				t.Fatalf("warnings logged = %d, want %d", got, tt.wantWarns)
			}

			if tt.wantWarns == 0 {
				return
			}

			entry := logs.All()[0]
			if !strings.Contains(entry.Message, "BOT__ALLOWED_USER_IDS") {
				t.Fatalf("warning = %q, want it to name BOT__ALLOWED_USER_IDS", entry.Message)
			}
		})
	}
}

package config_test

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"github.com/android-sms-gateway/telegram-bridge/internal/config"
	"github.com/android-sms-gateway/telegram-bridge/internal/webhook"
	"github.com/go-core-fx/cachefx"
	"github.com/go-core-fx/telegofx"
	"github.com/mymmrac/telego"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// testToken matches the telego token format ^\d+:[\w-]{35}$ and is never used
// against the real Telegram API: the graph under test only constructs the bot.
const testToken = "1234567890:AAAAAAAAAABBBBBBBBBBCCCCCCCCCCDDDDD"

// newBotError wires the config module into telegofx.New and forces bot
// construction, mirroring the serve graph without starting the poller.
func newBotError() error {
	app := fx.New(
		fx.NopLogger,
		config.Module(),
		fx.Supply(zap.NewNop()),
		fx.Supply([]telego.BotOption{}),
		fx.Provide(telegofx.New),
		fx.Invoke(func(_ *telegofx.Bot) {}),
	)

	return app.Err()
}

// TestTelegofxProxyMapping asserts that the raw TELEGRAM__PROXY_URL value reaches
// telegofx.Config.ProxyURL and that telegofx only accepts SOCKS5 proxy URLs.
func TestTelegofxProxyMapping(t *testing.T) {
	t.Setenv("TELEGRAM__TOKEN", testToken)

	tests := []struct {
		name    string
		unset   bool
		proxy   string
		wantErr string
	}{
		{
			name:    "unset proxy dials directly",
			unset:   true,
			proxy:   "",
			wantErr: "",
		},
		{
			name:    "empty proxy dials directly",
			unset:   false,
			proxy:   "",
			wantErr: "",
		},
		{
			name:    "socks5 proxy is accepted",
			unset:   false,
			proxy:   "socks5://127.0.0.1:1080",
			wantErr: "",
		},
		{
			name:    "socks5h proxy with auth is accepted",
			unset:   false,
			proxy:   "socks5h://user:pass@127.0.0.1:1080",
			wantErr: "",
		},
		{
			name:    "http proxy is rejected",
			unset:   false,
			proxy:   "http://127.0.0.1:8080",
			wantErr: `unsupported proxy scheme "http"`,
		},
		{
			name:    "https proxy is rejected",
			unset:   false,
			proxy:   "https://127.0.0.1:8443",
			wantErr: `unsupported proxy scheme "https"`,
		},
		{
			name:    "missing scheme is a parse error",
			unset:   false,
			proxy:   "127.0.0.1:8080",
			wantErr: `parse proxy url: parse "127.0.0.1:8080"`,
		},
		{
			name:    "socks5 without host is rejected",
			unset:   false,
			proxy:   "socks5://",
			wantErr: "proxy host is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv registers the restore hook; Unsetenv then leaves the
			// variable genuinely absent for the default-config case.
			t.Setenv("TELEGRAM__PROXY_URL", tt.proxy)

			if tt.unset {
				if err := os.Unsetenv("TELEGRAM__PROXY_URL"); err != nil {
					t.Fatalf("unset TELEGRAM__PROXY_URL: %v", err)
				}
			}

			err := newBotError()

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("bot construction error = %v, want nil", err)
				}

				return
			}

			if err == nil {
				t.Fatal("bot construction error = nil, want error")
			}

			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("bot construction error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// loadModuleConfigs builds the config module and captures every value it
// provides to a consumer, so one test can assert the whole raw -> consumer
// mapping without constructing a bot or an HTTP server.
func loadModuleConfigs(t *testing.T) (telegofx.Config, bridge.Config, webhook.Config, cachefx.Config) {
	t.Helper()

	var (
		gotTelegofx telegofx.Config
		gotBridge   bridge.Config
		gotWebhook  webhook.Config
		gotCache    cachefx.Config
	)

	app := fx.New(
		fx.NopLogger,
		config.Module(),
		fx.Invoke(func(tf telegofx.Config, bg bridge.Config, wh webhook.Config, cc cachefx.Config) {
			gotTelegofx = tf
			gotBridge = bg
			gotWebhook = wh
			gotCache = cc
		}),
	)

	if err := app.Err(); err != nil {
		t.Fatalf("config module error = %v", err)
	}

	return gotTelegofx, gotBridge, gotWebhook, gotCache
}

// TestModuleConfigMapping asserts the raw koanf/env surface reaches each
// consumer in the shape that consumer takes: the telegram section as
// telegofx.Config, the bridge section as bridge.Config, the cache section as
// cachefx.Config, the webhook signing key as webhook.Config.
func TestModuleConfigMapping(t *testing.T) {
	t.Setenv("TELEGRAM__TOKEN", testToken)
	t.Setenv("TELEGRAM__PROXY_URL", "socks5h://127.0.0.1:1080")
	t.Setenv("BRIDGE__CHAT_ID", "123456789")
	t.Setenv("CACHE__URL", "redis://127.0.0.1:6379")
	t.Setenv("CACHE__SWEEP_INTERVAL", "30s")
	t.Setenv("SMSGATE__WEBHOOK__SIGNING_KEY", "env-signing-key")

	gotTelegofx, gotBridge, gotWebhook, gotCache := loadModuleConfigs(t)

	wantTelegofx := telegofx.Config{
		Token:    testToken,
		ProxyURL: "socks5h://127.0.0.1:1080",
		Mode:     telegofx.ModePolling,
	}
	if gotTelegofx != wantTelegofx {
		t.Fatalf("telegofx.Config = %+v, want %+v", gotTelegofx, wantTelegofx)
	}

	if gotBridge != (bridge.Config{ChatID: 123456789}) {
		t.Fatalf("bridge.Config = %+v, want %+v", gotBridge, bridge.Config{ChatID: 123456789})
	}

	wantCache := cachefx.Config{URL: "redis://127.0.0.1:6379", SweepInterval: 30 * time.Second}
	if gotCache != wantCache {
		t.Fatalf("cachefx.Config = %+v, want %+v", gotCache, wantCache)
	}

	if gotWebhook != (webhook.Config{SigningKey: "env-signing-key"}) {
		t.Fatalf("webhook.Config = %+v, want %+v", gotWebhook, webhook.Config{SigningKey: "env-signing-key"})
	}
}

// clearChatIDEnv removes both spellings of the forward target from the process
// environment for the duration of the test: BRIDGE__CHAT_ID is the bridge
// section key, TELEGRAM__CHAT_ID the key of the former combined telegram
// section, which the raw config no longer declares.
func clearChatIDEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{"BRIDGE__CHAT_ID", "TELEGRAM__CHAT_ID"} {
		// t.Setenv registers the restore hook; Unsetenv then leaves the
		// variable genuinely absent.
		t.Setenv(key, "")

		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}
}

// TestBridgeChatIDSectionIsolation asserts the forward target is read from the
// bridge section only: BRIDGE__CHAT_ID reaches bridge.Config, while the legacy
// TELEGRAM__CHAT_ID of the former combined telegram section is inert.
func TestBridgeChatIDSectionIsolation(t *testing.T) {
	tests := []struct {
		name string
		set  map[string]string
		want int64
	}{
		{
			name: "bridge key is mapped",
			set:  map[string]string{"BRIDGE__CHAT_ID": "123456789"},
			want: 123456789,
		},
		{
			name: "negative supergroup chat is mapped",
			set:  map[string]string{"BRIDGE__CHAT_ID": "-1001234567890"},
			want: -1001234567890,
		},
		{
			name: "legacy telegram key is inert",
			set:  map[string]string{"TELEGRAM__CHAT_ID": "123456789"},
			want: 0,
		},
		{
			name: "both keys set keeps the bridge one",
			set:  map[string]string{"TELEGRAM__CHAT_ID": "1", "BRIDGE__CHAT_ID": "42"},
			want: 42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearChatIDEnv(t)

			for key, value := range tt.set {
				t.Setenv(key, value)
			}

			_, gotBridge, _, _ := loadModuleConfigs(t)

			if gotBridge != (bridge.Config{ChatID: tt.want}) {
				t.Fatalf("bridge.Config = %+v, want %+v", gotBridge, bridge.Config{ChatID: tt.want})
			}
		})
	}
}

// TestModuleConfigDefaults asserts the provided values reflect the raw defaults
// when nothing is configured, including that the webhook signing key stays
// empty, which makes signature verification fail closed.
func TestModuleConfigDefaults(t *testing.T) {
	envKeys := []string{
		"TELEGRAM__TOKEN",
		"TELEGRAM__PROXY_URL",
		"BRIDGE__CHAT_ID",
		"TELEGRAM__CHAT_ID",
		"CACHE__URL",
		"CACHE__SWEEP_INTERVAL",
		"SMSGATE__WEBHOOK__SIGNING_KEY",
	}

	tests := []struct {
		name         string
		unset        []string
		wantTelegofx telegofx.Config
	}{
		{
			name:  "unset env uses raw defaults",
			unset: envKeys,
			wantTelegofx: telegofx.Config{
				Token:    "",
				ProxyURL: "",
				Mode:     telegofx.ModePolling,
			},
		},
		{
			name:  "empty env matches raw defaults",
			unset: []string{"CACHE__URL", "CACHE__SWEEP_INTERVAL"},
			wantTelegofx: telegofx.Config{
				Token:    "",
				ProxyURL: "",
				Mode:     telegofx.ModePolling,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, key := range envKeys {
				t.Setenv(key, "")

				if slices.Contains(tt.unset, key) {
					if err := os.Unsetenv(key); err != nil {
						t.Fatalf("unset %s: %v", key, err)
					}
				}
			}

			gotTelegofx, gotBridge, gotWebhook, gotCache := loadModuleConfigs(t)

			if gotTelegofx != tt.wantTelegofx {
				t.Fatalf("telegofx.Config = %+v, want %+v", gotTelegofx, tt.wantTelegofx)
			}

			if gotBridge != (bridge.Config{ChatID: 0}) {
				t.Fatalf("bridge.Config = %+v, want %+v", gotBridge, bridge.Config{ChatID: 0})
			}

			wantCache := cachefx.Config{URL: "memory://", SweepInterval: time.Minute}
			if gotCache != wantCache {
				t.Fatalf("cachefx.Config = %+v, want %+v", gotCache, wantCache)
			}

			if gotWebhook != (webhook.Config{SigningKey: ""}) {
				t.Fatalf("webhook.Config = %+v, want empty signing key (fail-closed)", gotWebhook)
			}
		})
	}
}

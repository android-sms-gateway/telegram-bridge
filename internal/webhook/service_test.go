package webhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"github.com/android-sms-gateway/telegram-bridge/internal/store"
	"github.com/android-sms-gateway/telegram-bridge/internal/webhook"
	"github.com/android-sms-gateway/telegram-bridge/pkg/boltfx"
	"github.com/go-core-fx/telegofx"
	"github.com/mymmrac/telego"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	bolt "go.etcd.io/bbolt"
	"go.uber.org/zap"
)

const (
	// testBotToken matches the telego token format ^\d+:[\w-]{35}$ and is never
	// used against the real Telegram API (WithAPIServer redirects all calls).
	testBotToken = "1234567890:AAAAAAAAAABBBBBBBBBBCCCCCCCCCCDDDDD"

	testSigningKey       = "test-signing-key"
	testChatID     int64 = 123456789
	testTimestamp        = "1700000000"
	testSender           = "6505551212"
	testMessage          = "hello"
)

// sign computes the SMSGate payload signature: hex HMAC-SHA256 over the raw
// body followed by the fixed test timestamp string.
func sign(key, body string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(body))
	mac.Write([]byte(testTimestamp))

	return hex.EncodeToString(mac.Sum(nil))
}

// newTestBot creates a bot whose API calls go to the given URL.
func newTestBot(t *testing.T, apiURL string) *telegofx.Bot {
	t.Helper()

	bot, err := telegofx.New(
		telegofx.Config{Token: testBotToken, Mode: telegofx.ModePolling, ProxyURL: ""},
		[]telego.BotOption{telego.WithAPIServer(apiURL)},
		zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}

	return bot
}

// newTelegramStub starts a Telegram API stub answering sendMessage calls with
// the given message id.
func newTelegramStub(t *testing.T, status, messageID int) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":` + strconv.Itoa(messageID) + `}}`))
	}))
	t.Cleanup(server.Close)

	return server
}

// newTestStore opens the mapping repository the service writes to after a
// forward, wired the way pkg/boltfx and internal/store are in the serve graph.
// The raw handle is returned so a test can close the database and force a write
// failure.
func newTestStore(t *testing.T) (*store.Repository, *bolt.DB) {
	t.Helper()

	db, err := boltfx.New(boltfx.Config{URL: "bolt://" + filepath.Join(t.TempDir(), "service.db")}, zap.NewNop())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	// A second Close on a bbolt handle is a no-op, so a test that closed the
	// database on purpose does not report here.
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close database: %v", cerr)
		}
	})

	repo, err := store.NewRepository(store.Config{TTL: store.DefaultTTL}, db, zap.NewNop())
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}

	return repo, db
}

// counterValue reads the current value of one total counter label.
func counterValue(m *webhook.Metrics, result string) float64 {
	return testutil.ToFloat64(m.TotalCounter().WithLabelValues(result))
}

func TestService_Verify(t *testing.T) {
	t.Parallel()

	const body = `{"payload":{"sender":"` + testSender + `","message":"` + testMessage + `"}}`

	tests := []struct {
		name         string
		signingKey   string
		signature    string
		timestamp    string
		body         string
		wantErr      bool
		wantRejected float64
	}{
		{
			name:         "valid signature passes",
			signingKey:   testSigningKey,
			signature:    sign(testSigningKey, body),
			timestamp:    testTimestamp,
			body:         body,
			wantErr:      false,
			wantRejected: 0,
		},
		{
			name:         "missing signature",
			signingKey:   testSigningKey,
			signature:    "",
			timestamp:    testTimestamp,
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "missing timestamp",
			signingKey:   testSigningKey,
			signature:    sign(testSigningKey, body),
			timestamp:    "",
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "wrong key",
			signingKey:   testSigningKey,
			signature:    sign("other-key", body),
			timestamp:    testTimestamp,
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "tampered body",
			signingKey:   testSigningKey,
			signature:    sign(testSigningKey, `{"payload":{"sender":"attacker","message":"evil"}}`),
			timestamp:    testTimestamp,
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "tampered timestamp",
			signingKey:   testSigningKey,
			signature:    sign(testSigningKey, body),
			timestamp:    "1700000001",
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "empty configured key denies every request",
			signingKey:   "",
			signature:    sign("", body),
			timestamp:    testTimestamp,
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "uppercase signature rejected",
			signingKey:   testSigningKey,
			signature:    strings.ToUpper(sign(testSigningKey, body)),
			timestamp:    testTimestamp,
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			metrics := webhook.NewMetricsWithRegisterer(prometheus.NewRegistry())
			service := webhook.New(webhook.Config{SigningKey: tt.signingKey}, nil, nil, metrics, zap.NewNop())

			err := service.Verify([]byte(tt.body), tt.signature, tt.timestamp)
			if tt.wantErr {
				if err == nil {
					t.Fatal("Verify() error = nil, want error")
				}

				if !errors.Is(err, webhook.ErrInvalidSignature) {
					t.Fatalf("Verify() error = %v, want ErrInvalidSignature", err)
				}
			} else if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}

			if got := counterValue(metrics, "rejected"); got != tt.wantRejected {
				t.Fatalf("rejected counter = %v, want %v", got, tt.wantRejected)
			}
		})
	}
}

func TestService_Forward(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		chatID          int64
		stubStatus      int
		messageID       int
		wantErr         bool
		wantTotal       string
		wantTotalValue  float64
		wantForwardErrs float64
		wantMapping     bool
	}{
		{
			name:            "successful forward counts ok and stores the mapping",
			chatID:          testChatID,
			stubStatus:      http.StatusOK,
			messageID:       4242,
			wantErr:         false,
			wantTotal:       "ok",
			wantTotalValue:  1,
			wantForwardErrs: 0,
			wantMapping:     true,
		},
		{
			name:            "failed forward counts error",
			chatID:          testChatID,
			stubStatus:      http.StatusInternalServerError,
			messageID:       4242,
			wantErr:         true,
			wantTotal:       "error",
			wantTotalValue:  1,
			wantForwardErrs: 1,
			wantMapping:     false,
		},
		{
			name:            "unconfigured chat counts error",
			chatID:          0,
			stubStatus:      http.StatusOK,
			messageID:       4242,
			wantErr:         true,
			wantTotal:       "error",
			wantTotalValue:  1,
			wantForwardErrs: 1,
			wantMapping:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			metrics := webhook.NewMetricsWithRegisterer(prometheus.NewRegistry())
			stub := newTelegramStub(t, tt.stubStatus, tt.messageID)
			// Forward-only construction: the reply allowlist is empty, and the
			// reply collaborators are nil because no case here answers a bubble.
			forwarder := bridge.New(
				bridge.Config{ChatID: tt.chatID},
				newTestBot(t, stub.URL),
				nil,
				nil,
				zap.NewNop(),
			)
			repo, _ := newTestStore(t)
			service := webhook.New(webhook.Config{SigningKey: testSigningKey}, forwarder, repo, metrics, zap.NewNop())

			err := service.Forward(context.Background(), testSender, testMessage)
			if tt.wantErr {
				if err == nil {
					t.Fatal("Forward() error = nil, want error")
				}

				if chatID := tt.chatID; chatID == 0 && !errors.Is(err, bridge.ErrNoChatID) {
					t.Fatalf("Forward() error = %v, want wrapping ErrNoChatID", err)
				}
			} else if err != nil {
				t.Fatalf("Forward() error = %v", err)
			}

			if got := counterValue(metrics, tt.wantTotal); got != tt.wantTotalValue {
				t.Fatalf("%s counter = %v, want %v", tt.wantTotal, got, tt.wantTotalValue)
			}

			if got := testutil.ToFloat64(metrics.ForwardErrorsCounter()); got != tt.wantForwardErrs {
				t.Fatalf("forward_errors counter = %v, want %v", got, tt.wantForwardErrs)
			}

			// The mapping must exist exactly when Telegram accepted the message:
			// it is what a reply to the forwarded bubble resolves through.
			mapping, rerr := repo.Resolve(context.Background(), tt.chatID, tt.messageID)
			if tt.wantMapping {
				if rerr != nil {
					t.Fatalf("Resolve() of the stored mapping error = %v", rerr)
				}

				if mapping.Phone != testSender {
					t.Fatalf("mapping phone = %q, want %q", mapping.Phone, testSender)
				}

				// The store applies its own TTL, so the service writes no expiry.
				if !mapping.ExpiresAt.After(mapping.CreatedAt) {
					t.Fatalf("mapping expires_at = %v, want after created_at %v", mapping.ExpiresAt, mapping.CreatedAt)
				}
			} else if !errors.Is(rerr, store.ErrMappingNotFound) {
				t.Fatalf("Resolve() error = %v, want ErrMappingNotFound", rerr)
			}
		})
	}
}

// TestService_ForwardSurvivesStoreFailure pins the delivery semantics of a
// mapping write that fails after Telegram already accepted the message: the SMS
// is in the chat, so the webhook still answers 200. Failing the request instead
// would make the device retry the delivery and forward the same SMS twice.
func TestService_ForwardSurvivesStoreFailure(t *testing.T) {
	t.Parallel()

	metrics := webhook.NewMetricsWithRegisterer(prometheus.NewRegistry())
	stub := newTelegramStub(t, http.StatusOK, 77)
	// Forward-only construction: the reply allowlist is empty, and the reply
	// collaborators are nil because no case here answers a bubble.
	forwarder := bridge.New(
		bridge.Config{ChatID: testChatID},
		newTestBot(t, stub.URL),
		nil,
		nil,
		zap.NewNop(),
	)

	repo, db := newTestStore(t)

	// Closing the handle makes every later write fail with ErrDatabaseNotOpen.
	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	service := webhook.New(webhook.Config{SigningKey: testSigningKey}, forwarder, repo, metrics, zap.NewNop())

	if err := service.Forward(context.Background(), testSender, testMessage); err != nil {
		t.Fatalf("Forward() error = %v, want nil (the SMS is already in the chat)", err)
	}

	if got := counterValue(metrics, "ok"); got != 1 {
		t.Fatalf("ok counter = %v, want 1", got)
	}

	if got := counterValue(metrics, "error"); got != 0 {
		t.Fatalf("error counter = %v, want 0", got)
	}

	if got := testutil.ToFloat64(metrics.ForwardErrorsCounter()); got != 0 {
		t.Fatalf("forward_errors counter = %v, want 0", got)
	}
}

// TestService_ForwardKeepsLatestMapping asserts a repeated delivery to the same
// Telegram message id replaces the mapping instead of failing, which keeps a
// re-delivered webhook from being answered with an error.
func TestService_ForwardKeepsLatestMapping(t *testing.T) {
	t.Parallel()

	metrics := webhook.NewMetricsWithRegisterer(prometheus.NewRegistry())
	stub := newTelegramStub(t, http.StatusOK, 5)
	// Forward-only construction: the reply allowlist is empty, and the reply
	// collaborators are nil because no case here answers a bubble.
	forwarder := bridge.New(
		bridge.Config{ChatID: testChatID},
		newTestBot(t, stub.URL),
		nil,
		nil,
		zap.NewNop(),
	)

	repo, _ := newTestStore(t)
	service := webhook.New(webhook.Config{SigningKey: testSigningKey}, forwarder, repo, metrics, zap.NewNop())

	before := time.Now().Add(-time.Second)

	for _, phone := range []string{"6505551212", "6505551213"} {
		if err := service.Forward(context.Background(), phone, testMessage); err != nil {
			t.Fatalf("Forward() error = %v", err)
		}
	}

	mapping, err := repo.Resolve(context.Background(), testChatID, 5)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if mapping.Phone != "6505551213" {
		t.Fatalf("mapping phone = %q, want the last forwarded sender", mapping.Phone)
	}

	if mapping.CreatedAt.Before(before) {
		t.Fatalf("mapping created_at = %v, want at or after %v", mapping.CreatedAt, before)
	}
}

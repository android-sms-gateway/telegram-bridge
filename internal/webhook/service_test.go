package webhook_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"github.com/android-sms-gateway/telegram-bridge/internal/webhook"
	"github.com/go-core-fx/telegofx"
	"github.com/mymmrac/telego"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"go.uber.org/zap"
)

const (
	// testBotToken matches the telego token format ^\d+:[\w-]{35}$ and is never
	// used against the real Telegram API (WithAPIServer redirects all calls).
	testBotToken = "1234567890:AAAAAAAAAABBBBBBBBBBCCCCCCCCCCDDDDD"

	testSigningKey       = "test-signing-key"
	testChatID     int64 = 123456789
	testSender           = "6505551212"
	testMessage          = "hello"
)

// timestampAt formats t as the Unix-seconds X-Timestamp value.
func timestampAt(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10)
}

// sign computes the SMSGate payload signature: hex HMAC-SHA256 over the raw
// body followed by the given timestamp string.
func sign(key, body, timestamp string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(body))
	mac.Write([]byte(timestamp))

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

// newTelegramStub starts a Telegram API stub answering sendMessage calls.
func newTelegramStub(t *testing.T, status int) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(server.Close)

	return server
}

// counterValue reads the current value of one total counter label.
func counterValue(m *webhook.Metrics, result string) float64 {
	return testutil.ToFloat64(m.TotalCounter().WithLabelValues(result))
}

func TestService_Verify(t *testing.T) {
	t.Parallel()

	const body = `{"payload":{"sender":"` + testSender + `","message":"` + testMessage + `"}}`

	now := time.Now()
	ts := timestampAt(now)
	staleTS := timestampAt(now.Add(-10 * time.Minute))
	futureTS := timestampAt(now.Add(10 * time.Minute))
	otherTS := timestampAt(now.Add(time.Minute))

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
			signature:    sign(testSigningKey, body, ts),
			timestamp:    ts,
			body:         body,
			wantErr:      false,
			wantRejected: 0,
		},
		{
			name:         "missing signature",
			signingKey:   testSigningKey,
			signature:    "",
			timestamp:    ts,
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "missing timestamp",
			signingKey:   testSigningKey,
			signature:    sign(testSigningKey, body, ts),
			timestamp:    "",
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "wrong key",
			signingKey:   testSigningKey,
			signature:    sign("other-key", body, ts),
			timestamp:    ts,
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "tampered body",
			signingKey:   testSigningKey,
			signature:    sign(testSigningKey, `{"payload":{"sender":"attacker","message":"evil"}}`, ts),
			timestamp:    ts,
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "mismatched timestamp",
			signingKey:   testSigningKey,
			signature:    sign(testSigningKey, body, otherTS),
			timestamp:    ts,
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "stale timestamp rejected",
			signingKey:   testSigningKey,
			signature:    sign(testSigningKey, body, staleTS),
			timestamp:    staleTS,
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "future timestamp rejected",
			signingKey:   testSigningKey,
			signature:    sign(testSigningKey, body, futureTS),
			timestamp:    futureTS,
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "malformed timestamp rejected",
			signingKey:   testSigningKey,
			signature:    sign(testSigningKey, body, "not-a-unix-timestamp"),
			timestamp:    "not-a-unix-timestamp",
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "empty configured key denies every request",
			signingKey:   "",
			signature:    sign("", body, ts),
			timestamp:    ts,
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
		{
			name:         "uppercase signature rejected",
			signingKey:   testSigningKey,
			signature:    strings.ToUpper(sign(testSigningKey, body, ts)),
			timestamp:    ts,
			body:         body,
			wantErr:      true,
			wantRejected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			metrics := webhook.NewMetricsWithRegisterer(prometheus.NewRegistry())
			service := webhook.New(webhook.Config{SigningKey: tt.signingKey}, nil, metrics, zap.NewNop())

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
		wantErr         bool
		wantTotal       string
		wantTotalValue  float64
		wantForwardErrs float64
	}{
		{
			name:            "successful forward counts ok",
			chatID:          testChatID,
			stubStatus:      http.StatusOK,
			wantErr:         false,
			wantTotal:       "ok",
			wantTotalValue:  1,
			wantForwardErrs: 0,
		},
		{
			name:            "failed forward counts error",
			chatID:          testChatID,
			stubStatus:      http.StatusInternalServerError,
			wantErr:         true,
			wantTotal:       "error",
			wantTotalValue:  1,
			wantForwardErrs: 1,
		},
		{
			name:            "unconfigured chat counts error",
			chatID:          0,
			stubStatus:      http.StatusOK,
			wantErr:         true,
			wantTotal:       "error",
			wantTotalValue:  1,
			wantForwardErrs: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			metrics := webhook.NewMetricsWithRegisterer(prometheus.NewRegistry())
			stub := newTelegramStub(t, tt.stubStatus)
			forwarder := bridge.New(bridge.Config{ChatID: tt.chatID}, newTestBot(t, stub.URL), zap.NewNop())
			service := webhook.New(webhook.Config{SigningKey: testSigningKey}, forwarder, metrics, zap.NewNop())

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
		})
	}
}

package webhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	webhookhandler "github.com/android-sms-gateway/telegram-bridge/internal/server/handlers/webhook"
	"github.com/android-sms-gateway/telegram-bridge/internal/webhook"
	"github.com/go-core-fx/fiberfx"
	"github.com/go-core-fx/fiberfx/validation"
	"github.com/go-core-fx/telegofx"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/mymmrac/telego"
	"go.uber.org/zap"
)

const (
	// testBotToken matches the telego token format ^\d+:[\w-]{35}$ and is never
	// used against the real Telegram API (WithAPIServer redirects all calls).
	testBotToken = "1234567890:AAAAAAAAAABBBBBBBBBBCCCCCCCCCCDDDDD"

	testSigningKey       = "test-signing-key"
	testChatID     int64 = 123456789
	testMessageID        = "abc123"
	testSender           = "6505551212"
	testMessage          = "hello <world> & friends"

	validBody = `{"id":"event-id","webhookId":"webhook-id","event":"sms:received",` +
		`"deviceId":"device-id","payload":{"messageId":"` + testMessageID +
		`","sender":"` + testSender + `","recipient":"+1234567890","simNumber":1,` +
		`"phoneNumber":"` + testSender + `","message":"` + testMessage + `",` +
		`"receivedAt":"2024-06-22T15:46:11.000+07:00"}}`

	// bodyWithoutEvent is the same payload without the envelope event field.
	bodyWithoutEvent = `{"payload":{"messageId":"` + testMessageID +
		`","sender":"` + testSender + `","recipient":"+1234567890","simNumber":1,` +
		`"phoneNumber":"` + testSender + `","message":"` + testMessage + `",` +
		`"receivedAt":"2024-06-22T15:46:11.000+07:00"}}`
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

// telegramSend mirrors the subset of the Telegram sendMessage body the bridge
// must set.
type telegramSend struct {
	ChatID    int64  `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

// fixture bundles a fiber app with a stub Telegram API server. sent receives
// one entry per sendMessage call the app performs.
type fixture struct {
	app  *fiber.App
	sent chan telegramSend
}

// newFixture wires the full handler stack against a stub Telegram API server.
// metrics must be shared across fixtures because promauto panics on duplicate
// registration in the default registry.
func newFixture(t *testing.T, signingKey string, m *webhook.Metrics) *fixture {
	t.Helper()

	f := &fixture{
		app:  nil,
		sent: make(chan telegramSend, 1),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var send telegramSend
		if err := json.NewDecoder(r.Body).Decode(&send); err != nil {
			t.Errorf("decode telegram request: %v", err)
		}

		f.sent <- send

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(server.Close)

	bot, err := telegofx.New(
		telegofx.Config{Token: testBotToken, Mode: telegofx.ModePolling, ProxyURL: ""},
		[]telego.BotOption{telego.WithAPIServer(server.URL)},
		zap.NewNop(),
	)
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}

	forwarder := bridge.New(bridge.Config{ChatID: testChatID}, bot, zap.NewNop())
	service := webhook.New(webhook.Config{SigningKey: signingKey}, forwarder, m, zap.NewNop())
	handler := webhookhandler.New(service, validator.New())

	f.app = fiber.New(fiber.Config{ErrorHandler: fiberfx.NewJSONErrorHandler(zap.NewNop())})
	v1 := f.app.Group("/api/v1")
	// Mirrors internal/server/module.go: the validation middleware is mounted on
	// the versioned group, outside the handler.
	v1.Use(validation.Middleware)
	handler.Register(v1)

	return f
}

func TestHandlerPost(t *testing.T) {
	t.Parallel()

	metrics := webhook.NewMetrics()

	now := time.Now()
	ts := timestampAt(now)
	staleTS := timestampAt(now.Add(-10 * time.Minute))

	tests := []struct {
		name        string
		signingKey  string
		signature   string
		timestamp   string
		body        string
		wantStatus  int
		wantPhone   string
		wantForward bool
	}{
		{
			name:        "missing signature",
			signingKey:  testSigningKey,
			signature:   "",
			timestamp:   "",
			body:        validBody,
			wantStatus:  fiber.StatusUnauthorized,
			wantPhone:   "",
			wantForward: false,
		},
		{
			name:        "wrong key",
			signingKey:  testSigningKey,
			signature:   sign("other-key", validBody, ts),
			timestamp:   ts,
			body:        validBody,
			wantStatus:  fiber.StatusUnauthorized,
			wantPhone:   "",
			wantForward: false,
		},
		{
			name:        "missing timestamp",
			signingKey:  testSigningKey,
			signature:   sign(testSigningKey, validBody, ts),
			timestamp:   "",
			body:        validBody,
			wantStatus:  fiber.StatusUnauthorized,
			wantPhone:   "",
			wantForward: false,
		},
		{
			name:        "stale timestamp",
			signingKey:  testSigningKey,
			signature:   sign(testSigningKey, validBody, staleTS),
			timestamp:   staleTS,
			body:        validBody,
			wantStatus:  fiber.StatusUnauthorized,
			wantPhone:   "",
			wantForward: false,
		},
		{
			name:        "tampered body",
			signingKey:  testSigningKey,
			signature:   sign(testSigningKey, strings.Replace(validBody, testMessage, "evil", 1), ts),
			timestamp:   ts,
			body:        validBody,
			wantStatus:  fiber.StatusUnauthorized,
			wantPhone:   "",
			wantForward: false,
		},
		{
			name:        "empty configured key denies all",
			signingKey:  "",
			signature:   sign("", validBody, ts),
			timestamp:   ts,
			body:        validBody,
			wantStatus:  fiber.StatusUnauthorized,
			wantPhone:   "",
			wantForward: false,
		},
		{
			name:        "valid signature forwards sender",
			signingKey:  testSigningKey,
			signature:   sign(testSigningKey, validBody, ts),
			timestamp:   ts,
			body:        validBody,
			wantStatus:  fiber.StatusOK,
			wantPhone:   testSender,
			wantForward: true,
		},
		{
			name:       "unsupported event is rejected",
			signingKey: testSigningKey,
			signature: sign(testSigningKey,
				`{"event":"sms:sent","payload":{"sender":"`+testSender+`","message":"hi"}}`, ts,
			),
			timestamp:   ts,
			body:        `{"event":"sms:sent","payload":{"sender":"` + testSender + `","message":"hi"}}`,
			wantStatus:  fiber.StatusBadRequest,
			wantPhone:   "",
			wantForward: false,
		},
		{
			name:        "missing event is rejected",
			signingKey:  testSigningKey,
			signature:   sign(testSigningKey, bodyWithoutEvent, ts),
			timestamp:   ts,
			body:        bodyWithoutEvent,
			wantStatus:  fiber.StatusBadRequest,
			wantPhone:   "",
			wantForward: false,
		},
		{
			name:       "falls back to deprecated phone number",
			signingKey: testSigningKey,
			signature: sign(testSigningKey,
				`{"event":"sms:received","payload":{"phoneNumber":"`+testSender+`","message":"legacy"}}`, ts,
			),
			timestamp:   ts,
			body:        `{"event":"sms:received","payload":{"phoneNumber":"` + testSender + `","message":"legacy"}}`,
			wantStatus:  fiber.StatusOK,
			wantPhone:   testSender,
			wantForward: true,
		},
		{
			name:       "missing message",
			signingKey: testSigningKey,
			signature: sign(testSigningKey,
				`{"event":"sms:received","payload":{"sender":"`+testSender+`"}}`, ts,
			),
			timestamp:   ts,
			body:        `{"event":"sms:received","payload":{"sender":"` + testSender + `"}}`,
			wantStatus:  fiber.StatusBadRequest,
			wantPhone:   "",
			wantForward: false,
		}, {
			name:       "missing phone number",
			signingKey: testSigningKey,
			signature: sign(testSigningKey,
				`{"event":"sms:received","payload":{"message":"hi"}}`, ts,
			),
			timestamp:   ts,
			body:        `{"event":"sms:received","payload":{"message":"hi"}}`,
			wantStatus:  fiber.StatusBadRequest,
			wantPhone:   "",
			wantForward: false,
		},
		{
			name:        "missing payload",
			signingKey:  testSigningKey,
			signature:   sign(testSigningKey, `{"event":"sms:received"}`, ts),
			timestamp:   ts,
			body:        `{"event":"sms:received"}`,
			wantStatus:  fiber.StatusBadRequest,
			wantPhone:   "",
			wantForward: false,
		},
		{
			// validation.DecorateWithBodyEx hands a JSON syntax error back as
			// is: it is neither a validation.Errors (which the group-level
			// validation.Middleware maps to 400) nor a *fiber.Error (which the
			// JSON error handler classifies as 4xx), so it lands on 500. This
			// matches the template's example handler behavior.
			name:        "malformed json",
			signingKey:  testSigningKey,
			signature:   sign(testSigningKey, "{", ts),
			timestamp:   ts,
			body:        "{",
			wantStatus:  fiber.StatusInternalServerError,
			wantPhone:   "",
			wantForward: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, tt.signingKey, metrics)

			req := httptest.NewRequest(fiber.MethodPost, "/api/v1/webhook", strings.NewReader(tt.body))
			req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
			if tt.signature != "" {
				req.Header.Set("X-Signature", tt.signature)
			}

			if tt.timestamp != "" {
				req.Header.Set("X-Timestamp", tt.timestamp)
			}

			resp, err := f.app.Test(req, -1)
			if err != nil {
				t.Fatalf("app.Test() error = %v", err)
			}

			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}

			if tt.wantForward {
				select {
				case got := <-f.sent:
					if got.ChatID != testChatID {
						t.Fatalf("chat_id = %d, want %d", got.ChatID, testChatID)
					}

					if got.ParseMode != telego.ModeHTML {
						t.Fatalf("parse_mode = %q, want %q", got.ParseMode, telego.ModeHTML)
					}

					wantText := bridge.FormatForwardMessage(tt.wantPhone, extractMessage(tt.body))
					if got.Text != wantText {
						t.Fatalf("text = %q, want %q", got.Text, wantText)
					}
				default:
					t.Fatal("expected a Telegram forward, got none")
				}
			} else {
				select {
				case got := <-f.sent:
					t.Fatalf("unexpected Telegram forward: %+v", got)
				default:
				}
			}
		})
	}
}

// extractMessage pulls the SMS text out of a request body for the expected
// forward text assertion.
func extractMessage(body string) string {
	const key = `"message":"`

	start := strings.Index(body, key)
	if start < 0 {
		return ""
	}

	start += len(key)

	end := strings.Index(body[start:], `"`)
	if end < 0 {
		return ""
	}

	return body[start : start+end]
}

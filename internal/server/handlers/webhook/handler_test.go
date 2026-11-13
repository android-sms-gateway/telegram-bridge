package webhook_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	webhookhandler "github.com/android-sms-gateway/telegram-bridge/internal/server/handlers/webhook"
	"github.com/android-sms-gateway/telegram-bridge/internal/store"
	"github.com/android-sms-gateway/telegram-bridge/internal/webhook"
	"github.com/go-core-fx/boltfx"
	"github.com/go-core-fx/cachefx/cache"
	"github.com/go-core-fx/fiberfx"
	"github.com/go-core-fx/fiberfx/validation"
	"github.com/go-core-fx/telegofx"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/mymmrac/telego"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
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
	bodyWithoutEvent = `{"id":"event-id","payload":{"messageId":"` + testMessageID +
		`","sender":"` + testSender + `","recipient":"+1234567890","simNumber":1,` +
		`"phoneNumber":"` + testSender + `","message":"` + testMessage + `",` +
		`"receivedAt":"2024-06-22T15:46:11.000+07:00"}}`

	// bodyWithoutID is the same envelope without the delivery id.
	bodyWithoutID = `{"webhookId":"webhook-id","event":"sms:received",` +
		`"deviceId":"device-id","payload":{"messageId":"` + testMessageID +
		`","sender":"` + testSender + `","message":"` + testMessage + `"}}`

	// legacyBody exercises the deprecated phoneNumber alias. It still carries a
	// delivery id, which is required for de-duplication.
	legacyBody = `{"id":"legacy-event-id","webhookId":"webhook-id","event":"sms:received",` +
		`"payload":{"phoneNumber":"` + testSender + `","message":"legacy"}}`
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

// newTestRepo opens the mapping store the webhook service writes to after a
// forward, exactly as pkg/boltfx and internal/store do in the serve graph.
func newTestRepo(t *testing.T) *store.Repository {
	t.Helper()

	db, err := boltfx.New(boltfx.Config{URL: "bolt://" + filepath.Join(t.TempDir(), "handler.db")}, zap.NewNop())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close database: %v", cerr)
		}
	})

	repo, err := store.NewRepository(store.Config{TTL: store.DefaultTTL}, db)
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}

	return repo
}

// fixture bundles a fiber app with a stub Telegram API server. sent receives
// one entry per sendMessage call the app performs; request receives one signal
// per HTTP request that reaches the versioned group, so a test can wait until a
// redelivery has entered the app before releasing an in-flight forward.
type fixture struct {
	app     *fiber.App
	sent    chan telegramSend
	request chan struct{}
}

// newFixture wires the full handler stack against a stub Telegram API server.
// metrics must be shared across fixtures because promauto panics on duplicate
// registration in the default registry. When stall is non-nil the Telegram
// stub waits for it to be closed before answering, holding a forward in flight.
func newFixture(
	t *testing.T,
	signingKey string,
	m *webhook.Metrics,
	logger *zap.Logger,
	stall chan struct{},
) *fixture {
	t.Helper()

	f := &fixture{
		app:     nil,
		sent:    make(chan telegramSend, 1),
		request: make(chan struct{}, 8),
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var send telegramSend
		if err := json.NewDecoder(r.Body).Decode(&send); err != nil {
			t.Errorf("decode telegram request: %v", err)
		}

		f.sent <- send

		if stall != nil {
			<-stall
		}

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

	// Forward-only construction: the reply collaborators are nil because no case
	// here answers a bubble.
	forwarder := bridge.New(
		bridge.Config{ChatID: testChatID},
		bot,
		nil,
		nil,
		zap.NewNop(),
	)
	storeSvc := store.New(newTestRepo(t), zap.NewNop())
	service := webhook.New(
		webhook.Config{SigningKey: signingKey},
		forwarder,
		cache.NewMemory(0),
		storeSvc,
		m,
		zap.NewNop(),
	)
	handler := webhookhandler.New(service, validator.New(), logger)

	f.app = fiber.New(fiber.Config{ErrorHandler: fiberfx.NewJSONErrorHandler(zap.NewNop())})
	v1 := f.app.Group("/api/v1")
	v1.Use(func(c *fiber.Ctx) error {
		select {
		case f.request <- struct{}{}:
		default:
		}

		return c.Next()
	})
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
				`{"id":"event-id","event":"sms:sent","payload":{"sender":"`+testSender+`","message":"hi"}}`, ts,
			),
			timestamp:   ts,
			body:        `{"id":"event-id","event":"sms:sent","payload":{"sender":"` + testSender + `","message":"hi"}}`,
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
			name:        "falls back to deprecated phone number",
			signingKey:  testSigningKey,
			signature:   sign(testSigningKey, legacyBody, ts),
			timestamp:   ts,
			body:        legacyBody,
			wantStatus:  fiber.StatusOK,
			wantPhone:   testSender,
			wantForward: true,
		},
		{
			name:       "missing message",
			signingKey: testSigningKey,
			signature: sign(testSigningKey,
				`{"id":"event-id","event":"sms:received","payload":{"sender":"`+testSender+`"}}`, ts,
			),
			timestamp:   ts,
			body:        `{"id":"event-id","event":"sms:received","payload":{"sender":"` + testSender + `"}}`,
			wantStatus:  fiber.StatusBadRequest,
			wantPhone:   "",
			wantForward: false,
		},
		{
			name:       "missing phone number",
			signingKey: testSigningKey,
			signature: sign(testSigningKey,
				`{"id":"event-id","event":"sms:received","payload":{"message":"hi"}}`, ts,
			),
			timestamp:   ts,
			body:        `{"id":"event-id","event":"sms:received","payload":{"message":"hi"}}`,
			wantStatus:  fiber.StatusBadRequest,
			wantPhone:   "",
			wantForward: false,
		},
		{
			name:        "missing payload",
			signingKey:  testSigningKey,
			signature:   sign(testSigningKey, `{"id":"event-id","event":"sms:received"}`, ts),
			timestamp:   ts,
			body:        `{"id":"event-id","event":"sms:received"}`,
			wantStatus:  fiber.StatusBadRequest,
			wantPhone:   "",
			wantForward: false,
		},
		{
			name:        "missing event id",
			signingKey:  testSigningKey,
			signature:   sign(testSigningKey, bodyWithoutID, ts),
			timestamp:   ts,
			body:        bodyWithoutID,
			wantStatus:  fiber.StatusBadRequest,
			wantPhone:   "",
			wantForward: false,
		},
		{
			// A JSON syntax error is answered with 400 Bad Request, matching
			// the validation middleware's handling of a malformed body.
			name:        "malformed json",
			signingKey:  testSigningKey,
			signature:   sign(testSigningKey, "{", ts),
			timestamp:   ts,
			body:        "{",
			wantStatus:  fiber.StatusBadRequest,
			wantPhone:   "",
			wantForward: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFixture(t, tt.signingKey, metrics, zap.NewNop(), nil)

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

func TestHandlerPost_LogsEnvelopeIDs(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.InfoLevel)
	f := newFixture(
		t,
		testSigningKey,
		webhook.NewMetricsWithRegisterer(prometheus.NewRegistry()),
		zap.New(core),
		nil,
	)

	ts := timestampAt(time.Now())

	req := httptest.NewRequest(fiber.MethodPost, "/api/v1/webhook", strings.NewReader(validBody))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	req.Header.Set("X-Signature", sign(testSigningKey, validBody, ts))
	req.Header.Set("X-Timestamp", ts)

	resp, err := f.app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}

	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}

	entries := logs.FilterMessage("webhook received").All()
	if len(entries) != 1 {
		t.Fatalf("webhook received entries = %d, want 1", len(entries))
	}

	want := map[string]string{
		"event":      "sms:received",
		"event_id":   "event-id",
		"webhook_id": "webhook-id",
		"device_id":  "device-id",
		"message_id": testMessageID,
	}

	got := entries[0].ContextMap()
	for key, wantValue := range want {
		if gotValue, ok := got[key]; !ok || gotValue != wantValue {
			t.Fatalf("%s = %v, want %q", key, gotValue, wantValue)
		}
	}

	if len(got) != len(want) {
		t.Fatalf("log fields = %v, want exactly %v", got, want)
	}
}

func TestHandlerPost_NoEnvelopeLogWithoutValidSignature(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zap.InfoLevel)
	f := newFixture(
		t,
		testSigningKey,
		webhook.NewMetricsWithRegisterer(prometheus.NewRegistry()),
		zap.New(core),
		nil,
	)

	ts := timestampAt(time.Now())

	req := httptest.NewRequest(fiber.MethodPost, "/api/v1/webhook", strings.NewReader(validBody))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	req.Header.Set("X-Signature", sign("other-key", validBody, ts))
	req.Header.Set("X-Timestamp", ts)

	resp, err := f.app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}

	if resp.StatusCode == fiber.StatusOK {
		t.Fatalf("status = %d, want a rejection", resp.StatusCode)
	}

	if n := len(logs.FilterMessage("webhook received").All()); n != 0 {
		t.Fatalf("webhook received entries = %d, want 0", n)
	}
}

// TestHandlerPost_DeduplicatesEventID checks that a redelivery of the same
// envelope id is acknowledged without a second Telegram forward.
func TestHandlerPost_DeduplicatesEventID(t *testing.T) {
	t.Parallel()

	f := newFixture(
		t,
		testSigningKey,
		webhook.NewMetricsWithRegisterer(prometheus.NewRegistry()),
		zap.NewNop(),
		nil,
	)

	ts := timestampAt(time.Now())

	resp := postWebhook(t, f, validBody, ts)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("first status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}

	select {
	case <-f.sent:
	default:
		t.Fatal("expected a Telegram forward for the first delivery")
	}

	resp = postWebhook(t, f, validBody, ts)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("duplicate status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}

	select {
	case got := <-f.sent:
		t.Fatalf("unexpected Telegram forward for the duplicate: %+v", got)
	default:
	}
}

// TestHandlerPost_DifferentEventIDsForward checks that a new envelope id is
// not suppressed by an earlier delivery.
func TestHandlerPost_DifferentEventIDsForward(t *testing.T) {
	t.Parallel()

	f := newFixture(
		t,
		testSigningKey,
		webhook.NewMetricsWithRegisterer(prometheus.NewRegistry()),
		zap.NewNop(),
		nil,
	)

	ts := timestampAt(time.Now())
	otherBody := strings.Replace(validBody, `"id":"event-id"`, `"id":"event-id-2"`, 1)

	resp := postWebhook(t, f, validBody, ts)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("first status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}

	select {
	case <-f.sent:
	default:
		t.Fatal("expected a Telegram forward for the first delivery")
	}

	resp = postWebhook(t, f, otherBody, ts)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("second status = %d, want %d", resp.StatusCode, fiber.StatusOK)
	}

	select {
	case <-f.sent:
	default:
		t.Fatal("expected a Telegram forward for the second delivery")
	}
}

// TestHandlerPost_ConcurrentDuplicateForwardsOnce checks that a redelivery
// arriving while the first delivery is still being forwarded waits for it
// instead of being rejected with a retryable error: the redelivery cannot
// finish early, both requests are answered with 200 and only one message
// reaches Telegram.
func TestHandlerPost_ConcurrentDuplicateForwardsOnce(t *testing.T) {
	t.Parallel()

	stall := make(chan struct{})
	release := sync.OnceFunc(func() { close(stall) })
	defer release()

	f := newFixture(
		t,
		testSigningKey,
		webhook.NewMetricsWithRegisterer(prometheus.NewRegistry()),
		zap.NewNop(),
		stall,
	)

	ts := timestampAt(time.Now())

	type result struct {
		status int
		err    error
	}

	firstDone := make(chan result, 1)
	go func() {
		status, err := tryPostWebhook(f, validBody, ts)
		firstDone <- result{status: status, err: err}
	}()

	// The first request reached the app and then the Telegram stub, so it is
	// now holding the forward lock.
	select {
	case <-f.request:
	case <-time.After(10 * time.Second):
		t.Fatal("first delivery never reached the app")
	}

	select {
	case <-f.sent:
	case <-time.After(10 * time.Second):
		t.Fatal("first delivery never reached Telegram")
	}

	secondDone := make(chan result, 1)
	go func() {
		status, err := tryPostWebhook(f, validBody, ts)
		secondDone <- result{status: status, err: err}
	}()

	// Wait until the redelivery has entered the app, then confirm it stays
	// blocked on the forward lock while the first delivery is stalled. The
	// final f.sent check proves no second forward happened.
	select {
	case <-f.request:
	case <-time.After(10 * time.Second):
		t.Fatal("redelivery never reached the app")
	}

	select {
	case res := <-secondDone:
		t.Fatalf("redelivery finished before the first forward was released: %+v", res)
	case <-time.After(200 * time.Millisecond):
	}

	release()

	for i, done := range []chan result{firstDone, secondDone} {
		select {
		case res := <-done:
			if res.err != nil {
				t.Fatalf("request #%d error = %v", i+1, res.err)
			}
			if res.status != fiber.StatusOK {
				t.Fatalf("request #%d status = %d, want %d", i+1, res.status, fiber.StatusOK)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("request #%d did not finish", i+1)
		}
	}

	select {
	case got := <-f.sent:
		t.Fatalf("unexpected second Telegram forward: %+v", got)
	default:
	}
}

// postWebhook sends a signed webhook request through the fixture app.
func postWebhook(t *testing.T, f *fixture, body, ts string) *http.Response {
	t.Helper()

	req := httptest.NewRequest(fiber.MethodPost, "/api/v1/webhook", strings.NewReader(body))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	req.Header.Set("X-Signature", sign(testSigningKey, body, ts))
	req.Header.Set("X-Timestamp", ts)

	resp, err := f.app.Test(req, -1)
	if err != nil {
		t.Fatalf("app.Test() error = %v", err)
	}

	return resp
}

// tryPostWebhook sends a signed webhook request without touching the test
// assertion machinery, so it is safe to call from a goroutine.
func tryPostWebhook(f *fixture, body, ts string) (int, error) {
	req := httptest.NewRequest(fiber.MethodPost, "/api/v1/webhook", strings.NewReader(body))
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	req.Header.Set("X-Signature", sign(testSigningKey, body, ts))
	req.Header.Set("X-Timestamp", ts)

	resp, err := f.app.Test(req, -1)
	if err != nil {
		return 0, err
	}

	return resp.StatusCode, nil
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

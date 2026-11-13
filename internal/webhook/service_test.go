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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"github.com/android-sms-gateway/telegram-bridge/internal/store"
	"github.com/android-sms-gateway/telegram-bridge/internal/webhook"
	"github.com/go-core-fx/boltfx"
	"github.com/go-core-fx/cachefx/cache"
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
	testSender           = "6505551212"
	testMessage          = "hello"
	testEventKey         = "webhook-id:event-id"
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

// stubCtl makes a Telegram stub signal when it receives a call and wait
// before answering, so a test can hold a forward in flight. Both channels are
// optional; release is idempotent so a failing test cannot leave the stub
// blocked past its cleanup.
type stubCtl struct {
	started chan struct{}
	stall   chan struct{}
	release func()
}

// newStubCtl builds a stubCtl that signals started once and blocks until the
// returned release func is called.
func newStubCtl() *stubCtl {
	stall := make(chan struct{})
	once := sync.Once{}

	return &stubCtl{
		started: make(chan struct{}, 1),
		stall:   stall,
		release: func() {
			once.Do(func() { close(stall) })
		},
	}
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

// newCountingTelegramStub starts a Telegram API stub answering sendMessage
// calls and returns a counter of the calls it received. When ctl is non-nil
// the stub signals ctl.started and waits for ctl.release before answering.
func newCountingTelegramStub(t *testing.T, status int, ctl *stubCtl) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	var calls atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)

		if ctl != nil {
			select {
			case ctl.started <- struct{}{}:
			default:
			}

			<-ctl.stall
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(server.Close)

	return server, &calls
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

	repo, err := store.NewRepository(store.Config{TTL: store.DefaultTTL}, db)
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
			service := webhook.New(
				webhook.Config{SigningKey: tt.signingKey},
				nil,
				cache.NewMemory(0),
				nil,
				metrics,
				zap.NewNop(),
			)

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
			// Forward-only construction: the reply collaborators are nil because
			// no case here answers a bubble.
			forwarder := bridge.New(
				bridge.Config{ChatID: tt.chatID},
				newTestBot(t, stub.URL),
				nil,
				nil,
				zap.NewNop(),
			)
			repo, _ := newTestStore(t)
			storeSvc := store.New(repo, zap.NewNop())
			service := webhook.New(
				webhook.Config{SigningKey: testSigningKey},
				forwarder,
				cache.NewMemory(0),
				storeSvc,
				metrics,
				zap.NewNop(),
			)

			err := service.Forward(context.Background(), testEventKey, testSender, testMessage)
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
			mapping, rerr := repo.Resolve(context.Background(), store.MessageRef{
				ChatID:    tt.chatID,
				MessageID: tt.messageID,
			})
			if tt.wantMapping {
				if rerr != nil {
					t.Fatalf("Resolve() of the stored mapping error = %v", rerr)
				}

				if mapping.Phone != testSender {
					t.Fatalf("mapping phone = %q, want %q", mapping.Phone, testSender)
				}
			} else if !errors.Is(rerr, store.ErrMappingNotFound) {
				t.Fatalf("Resolve() error = %v, want ErrMappingNotFound", rerr)
			}
		})
	}
}

// TestService_Forward_Deduplicates checks that a second delivery with the same
// event key is skipped and counted as a duplicate.
func TestService_Forward_Deduplicates(t *testing.T) {
	t.Parallel()

	metrics := webhook.NewMetricsWithRegisterer(prometheus.NewRegistry())
	stub, calls := newCountingTelegramStub(t, http.StatusOK, nil)
	forwarder := bridge.New(bridge.Config{ChatID: testChatID}, newTestBot(t, stub.URL), nil, nil, zap.NewNop())
	repo, _ := newTestStore(t)
	storeSvc := store.New(repo, zap.NewNop())
	service := webhook.New(
		webhook.Config{SigningKey: testSigningKey},
		forwarder,
		cache.NewMemory(0),
		storeSvc,
		metrics,
		zap.NewNop(),
	)

	ctx := context.Background()

	if err := service.Forward(ctx, testEventKey, testSender, testMessage); err != nil {
		t.Fatalf("first Forward() error = %v", err)
	}

	if err := service.Forward(ctx, testEventKey, testSender, testMessage); err != nil {
		t.Fatalf("duplicate Forward() error = %v, want nil", err)
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("telegram calls = %d, want 1", got)
	}

	if got := counterValue(metrics, "ok"); got != 1 {
		t.Fatalf("ok counter = %v, want 1", got)
	}

	if got := counterValue(metrics, "duplicate"); got != 1 {
		t.Fatalf("duplicate counter = %v, want 1", got)
	}
}

// TestService_Forward_ReleasesEventOnForwardError checks that a failed forward
// removes the dedup marker, so a later retry of the same event is not
// suppressed.
func TestService_Forward_ReleasesEventOnForwardError(t *testing.T) {
	t.Parallel()

	metrics := webhook.NewMetricsWithRegisterer(prometheus.NewRegistry())
	stub := newTelegramStub(t, http.StatusInternalServerError, 1)
	forwarder := bridge.New(bridge.Config{ChatID: testChatID}, newTestBot(t, stub.URL), nil, nil, zap.NewNop())
	eventCache := cache.NewMemory(0)
	repo, _ := newTestStore(t)
	storeSvc := store.New(repo, zap.NewNop())
	service := webhook.New(
		webhook.Config{SigningKey: testSigningKey},
		forwarder,
		eventCache,
		storeSvc,
		metrics,
		zap.NewNop(),
	)

	ctx := context.Background()

	if err := service.Forward(ctx, testEventKey, testSender, testMessage); err == nil {
		t.Fatal("Forward() error = nil, want error")
	}

	if _, err := eventCache.Get(ctx, testEventKey); !errors.Is(err, cache.ErrKeyNotFound) {
		t.Fatalf("event marker still present after failed forward, Get() error = %v", err)
	}
}

// TestService_Forward_ConcurrentDuplicateForwardsOnce checks that a redelivery
// arriving while the first forward is still running waits for it instead of
// forwarding alongside it: the redelivery cannot finish early, exactly one
// Telegram call happens, and both deliveries report success with the loser
// counted as a duplicate.
func TestService_Forward_ConcurrentDuplicateForwardsOnce(t *testing.T) {
	t.Parallel()

	metrics := webhook.NewMetricsWithRegisterer(prometheus.NewRegistry())
	ctl := newStubCtl()
	defer ctl.release()

	stub, calls := newCountingTelegramStub(t, http.StatusOK, ctl)
	forwarder := bridge.New(bridge.Config{ChatID: testChatID}, newTestBot(t, stub.URL), nil, nil, zap.NewNop())
	repo, _ := newTestStore(t)
	storeSvc := store.New(repo, zap.NewNop())
	service := webhook.New(
		webhook.Config{SigningKey: testSigningKey},
		forwarder,
		cache.NewMemory(0),
		storeSvc,
		metrics,
		zap.NewNop(),
	)

	ctx := context.Background()
	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)

	go func() {
		firstDone <- service.Forward(ctx, testEventKey, testSender, testMessage)
	}()

	select {
	case <-ctl.started:
	case <-time.After(10 * time.Second):
		t.Fatal("first forward never reached Telegram")
	}

	// The first delivery reached Telegram while holding the forward lock; the
	// redelivery must block there until the first one finishes.
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		secondDone <- service.Forward(ctx, testEventKey, testSender, testMessage)
	}()

	// Wait until the redelivery has started, then confirm it stays blocked
	// while the first forward is stalled. The final calls and counter
	// assertions prove the redelivery was skipped rather than forwarded
	// alongside the first.
	select {
	case <-secondStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("redelivery never started")
	}

	select {
	case err := <-secondDone:
		t.Fatalf("redelivery finished before the first forward was released: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	ctl.release()

	for i, done := range []chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("Forward() #%d error = %v, want nil", i+1, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("Forward() #%d did not finish", i+1)
		}
	}

	if got := calls.Load(); got != 1 {
		t.Fatalf("telegram calls = %d, want 1", got)
	}

	if got := counterValue(metrics, "ok"); got != 1 {
		t.Fatalf("ok counter = %v, want 1", got)
	}

	if got := counterValue(metrics, "duplicate"); got != 1 {
		t.Fatalf("duplicate counter = %v, want 1", got)
	}
}

// TestService_ForwardStoresMappingsForDeliveredPartsOnPartialFailure asserts a
// failure on a later chunk of a split message still maps the part Telegram
// accepted, and reports the failure: the device is told the delivery failed,
// but a reply to the part that is already in the chat still resolves to the
// sender.
func TestService_ForwardStoresMappingsForDeliveredPartsOnPartialFailure(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	requests := 0

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		if n == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":707}}`))

			return
		}

		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"stub failure"}`))
	}))
	t.Cleanup(stub.Close)

	metrics := webhook.NewMetricsWithRegisterer(prometheus.NewRegistry())
	// Forward-only construction: the reply collaborators are nil because no case
	// here answers a bubble.
	forwarder := bridge.New(
		bridge.Config{ChatID: testChatID},
		newTestBot(t, stub.URL),
		nil,
		nil,
		zap.NewNop(),
	)
	repo, _ := newTestStore(t)
	storeSvc := store.New(repo, zap.NewNop())
	service := webhook.New(
		webhook.Config{SigningKey: testSigningKey},
		forwarder,
		cache.NewMemory(0),
		storeSvc,
		metrics,
		zap.NewNop(),
	)

	// Well over the 4096-character Telegram limit (whatever the sender
	// overhead), so the forward always splits and the second Telegram call is
	// the one that fails.
	err := service.Forward(context.Background(), testEventKey, testSender, strings.Repeat("a", 5000))
	if err == nil {
		t.Fatal("Forward() error = nil, want the failure of the second chunk")
	}

	if got := counterValue(metrics, "error"); got != 1 {
		t.Fatalf("error counter = %v, want 1", got)
	}

	if got := testutil.ToFloat64(metrics.ForwardErrorsCounter()); got != 1 {
		t.Fatalf("forward_errors counter = %v, want 1", got)
	}

	// The delivered part must be mapped even though the forward failed: this is
	// the mapping a reply to the first bubble resolves through.
	mapping, rerr := repo.Resolve(context.Background(), store.MessageRef{ChatID: testChatID, MessageID: 707})
	if rerr != nil {
		t.Fatalf("Resolve() of the delivered part error = %v, want the mapping stored", rerr)
	}

	if mapping.Phone != testSender {
		t.Fatalf("mapping phone = %q, want %q", mapping.Phone, testSender)
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
	// Forward-only construction: the reply collaborators are nil because no case
	// here answers a bubble.
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

	storeSvc := store.New(repo, zap.NewNop())
	service := webhook.New(
		webhook.Config{SigningKey: testSigningKey},
		forwarder,
		cache.NewMemory(0),
		storeSvc,
		metrics,
		zap.NewNop(),
	)

	if err := service.Forward(context.Background(), testEventKey, testSender, testMessage); err != nil {
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
	// Forward-only construction: the reply collaborators are nil because no case
	// here answers a bubble.
	forwarder := bridge.New(
		bridge.Config{ChatID: testChatID},
		newTestBot(t, stub.URL),
		nil,
		nil,
		zap.NewNop(),
	)

	repo, _ := newTestStore(t)
	storeSvc := store.New(repo, zap.NewNop())
	service := webhook.New(
		webhook.Config{SigningKey: testSigningKey},
		forwarder,
		cache.NewMemory(0),
		storeSvc,
		metrics,
		zap.NewNop(),
	)

	// Distinct event keys: the same Telegram message id arriving under two
	// different deliveries must not be skipped by de-duplication.
	for i, phone := range []string{"6505551212", "6505551213"} {
		eventKey := "webhook-id:event-" + strconv.Itoa(i+1)
		if err := service.Forward(context.Background(), eventKey, phone, testMessage); err != nil {
			t.Fatalf("Forward() error = %v", err)
		}
	}

	mapping, err := repo.Resolve(context.Background(), store.MessageRef{ChatID: testChatID, MessageID: 5})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if mapping.Phone != "6505551213" {
		t.Fatalf("mapping phone = %q, want the last forwarded sender", mapping.Phone)
	}
}

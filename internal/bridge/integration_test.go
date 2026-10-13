// This file drives the whole reply path end to end through the telego adapter in
// internal/bot/handlers/reply. Those cases live here, next to the service they
// call, because the handler owns no repository of its own any more: it cannot
// build a mapping fixture without one, and a handler test that reached for the
// store would put the repository back in reach of a handler.

package bridge_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	botconfig "github.com/android-sms-gateway/telegram-bridge/internal/bot/config"
	replyhandler "github.com/android-sms-gateway/telegram-bridge/internal/bot/handlers/reply"
	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"github.com/android-sms-gateway/telegram-bridge/internal/smsgate"
	"github.com/android-sms-gateway/telegram-bridge/internal/store"
	"github.com/android-sms-gateway/telegram-bridge/internal/webhook"
	"github.com/go-core-fx/telegofx"
	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

const (
	defaultStubMessageID = 99

	// forwardedMessageID is the id the Telegram stub hands out for every forward,
	// which is what a reply to the forwarded bubble carries back.
	forwardedMessageID = 777

	// pollInterval and pollTimeout bound the wait for the handler goroutine, which
	// the telegohandler runs outside the test goroutine.
	pollInterval = 5 * time.Millisecond
	pollTimeout  = 5 * time.Second

	// graceInterval is how long a case that expects no output at all waits
	// before concluding that nothing was sent.
	graceInterval = 200 * time.Millisecond
)

// telegramSend mirrors the subset of the Telegram sendMessage body the handler
// must set.
type telegramSend struct {
	ChatID int64  `json:"chat_id"`
	Text   string `json:"text"`
}

// telegramStub answers sendMessage calls and records what was sent.
type telegramStub struct {
	messageID int

	mu   sync.Mutex
	sent []telegramSend
}

// telegramStubCount returns how many messages the handler sent to the chat.
func (s *telegramStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.sent)
}

// telegramStubLast returns the text of the most recent message, or an empty
// string when nothing was sent.
func (s *telegramStub) last() telegramSend {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.sent) == 0 {
		return telegramSend{}
	}

	return s.sent[len(s.sent)-1]
}

// newTelegramServer starts the Telegram API stub that records every sendMessage
// and answers with the given message id.
func newTelegramServer(t *testing.T, messageID int) (*telegramStub, string) {
	t.Helper()

	telegram := &telegramStub{messageID: messageID, mu: sync.Mutex{}, sent: nil}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read telegram request body: %v", err)
		}

		var send telegramSend
		if uerr := json.Unmarshal(body, &send); uerr != nil {
			t.Errorf("unmarshal telegram request body: %v", uerr)
		}

		telegram.mu.Lock()
		telegram.sent = append(telegram.sent, send)
		telegram.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":` +
			strconv.Itoa(telegram.messageID) + `}}`))
	}))
	t.Cleanup(server.Close)

	return telegram, server.URL
}

// updateJSON builds a Telegram update that replies to a forwarded bubble in the
// test chat.
func updateJSON(userID int64, text string) string {
	return updateJSONFor(userID, testChatID, testTGMessageID, text)
}

// updateJSONFor builds a Telegram update that replies to a specific forwarded
// bubble in a specific chat.
func updateJSONFor(userID, chatID, replyToID int64, text string) string {
	return fmt.Sprintf(`{"update_id":1,"message":{"message_id":10,`+
		`"from":{"id":%d,"is_bot":false,"first_name":"Operator"},`+
		`"chat":{"id":%d,"type":"private"},`+
		`"text":%q,`+
		`"reply_to_message":{"message_id":%d,"from":{"id":1,"is_bot":true,"first_name":"Bridge"},`+
		`"chat":{"id":%d,"type":"private"},"date":1700000000,`+
		`"text":"New message from <b>6505551212</b>"}}}`,
		userID, chatID, text, replyToID, chatID)
}

// plainMessageJSON builds a message update that is not a reply to anything.
func plainMessageJSON(userID int64, text string) string {
	return fmt.Sprintf(`{"update_id":1,"message":{"message_id":11,`+
		`"from":{"id":%d,"is_bot":false,"first_name":"Operator"},`+
		`"chat":{"id":%d,"type":"private"},"text":%q}}`,
		userID, testChatID, text)
}

// mediaReplyJSON builds a reply that carries a photo instead of text.
func mediaReplyJSON(userID int64) string {
	return fmt.Sprintf(`{"update_id":1,"message":{"message_id":10,`+
		`"from":{"id":%d,"is_bot":false,"first_name":"Operator"},`+
		`"chat":{"id":%d,"type":"private"},`+
		`"photo":[{"file_id":"abc","file_unique_id":"u1","width":90,"height":90}],`+
		`"reply_to_message":{"message_id":%d,"from":{"id":1,"is_bot":true,"first_name":"Bridge"},`+
		`"chat":{"id":%d,"type":"private"},"date":1700000000,"text":"forwarded"}}}`,
		userID, testChatID, testTGMessageID, testChatID)
}

// decodeUpdate turns a raw Telegram update body into an update value.
func decodeUpdate(t *testing.T, raw string) telego.Update {
	t.Helper()

	var update telego.Update
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatalf("unmarshal update: %v", err)
	}

	if update.Message == nil {
		t.Fatalf("update carries no message: %s", raw)
	}

	return update
}

// waitFor polls until cond holds or the timeout expires.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()

	deadline := time.Now().Add(pollTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(pollInterval)
	}

	t.Fatalf("timed out waiting for %s", what)
}

// handlerFixture bundles a running reply handler with the three collaborators it
// is built from: the durable store the forward path writes, the SMSGate server
// API and the Telegram API.
type handlerFixture struct {
	repo *store.Repository

	telegram *telegramStub
	sms      *smsStub

	updates chan telego.Update
}

// newHandlerFixture wires the telego adapter around a real bridge service,
// exactly as the serve graph does: the same store repository the forward path
// writes, the same smsgate service, and a running telegofx router.
func newHandlerFixture(t *testing.T, allowed []int64, responses ...smsResponse) *handlerFixture {
	t.Helper()

	telegram, apiURL := newTelegramServer(t, defaultStubMessageID)

	repo, _ := newRepo(t)
	sms := newSMSServer(t, responses...)

	// The forward half of the service is not exercised here, so it is built
	// without a bot: every case below only answers a forwarded bubble.
	service := bridge.New(
		bridge.Config{ChatID: 0},
		nil,
		repo,
		smsgate.New(smsgate.Config{
			BaseURL:  sms.url,
			Username: testUsername,
			Password: testPassword,

			Timeout: smsgate.DefaultTimeout,
		}, zap.NewNop()),
		zap.NewNop(),
	)

	updates := make(chan telego.Update, 1)
	botHandler, err := th.NewBotHandler(newTestBot(t, apiURL).Bot, updates)
	if err != nil {
		t.Fatalf("create bot handler: %v", err)
	}

	// The allowlist reaches the handler the way the graph delivers it: the
	// handler builds the filter of its route from the module config in its
	// constructor, and the router applies it while the route is registered.
	replyhandler.New(service, botconfig.Config{AllowedUserIDs: allowed}).Register(
		&telegofx.Router{BotHandler: botHandler},
	)

	go func() {
		if serr := botHandler.Start(); serr != nil && !strings.Contains(serr.Error(), "stopped") {
			t.Errorf("run bot handler: %v", serr)
		}
	}()

	t.Cleanup(func() {
		if serr := botHandler.Stop(); serr != nil {
			t.Errorf("stop bot handler: %v", serr)
		}
	})

	return &handlerFixture{
		repo: repo,

		telegram: telegram,
		sms:      sms,

		updates: updates,
	}
}

// storeMapping writes the mapping a forward would have written, for the sender
// and the expiry the case needs.
func (f *handlerFixture) storeMapping(t *testing.T, expiresAt time.Time) {
	t.Helper()

	err := f.repo.Put(context.Background(), testChatID, testTGMessageID, store.Mapping{
		Phone:     testPhone,
		CreatedAt: time.Now().Add(-time.Hour),
		ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("store mapping: %v", err)
	}
}

// deliver hands a raw Telegram update to the running handler.
func (f *handlerFixture) deliver(t *testing.T, raw string) {
	t.Helper()

	f.updates <- decodeUpdate(t, raw)
}

// TestHandleReply_SendsToMappedSender covers the round trip of a single update:
// an allowed user answers a forwarded bubble, the answer goes out to the mapped
// sender and the chat is told which message was queued.
func TestHandleReply_SendsToMappedSender(t *testing.T) {
	t.Parallel()

	f := newHandlerFixture(t, []int64{testAllowedID}, queued())
	f.storeMapping(t, time.Time{})

	f.deliver(t, updateJSON(testAllowedID, testReplyText))

	waitFor(t, func() bool { return f.telegram.count() == 1 }, "the acknowledgement")

	if got := f.sms.count(); got != 1 {
		t.Fatalf("sms sends = %d, want 1", got)
	}

	send := f.sms.last()
	if len(send.PhoneNumbers) != 1 || send.PhoneNumbers[0] != testPhone {
		t.Fatalf("phone numbers = %v, want [%s]", send.PhoneNumbers, testPhone)
	}

	if send.TextMessage.Text != testReplyText {
		t.Fatalf("text = %q, want %q", send.TextMessage.Text, testReplyText)
	}

	ack := f.telegram.last()
	if ack.ChatID != testChatID {
		t.Fatalf("ack chat id = %d, want %d", ack.ChatID, testChatID)
	}

	if !strings.Contains(ack.Text, testPhone) || !strings.Contains(ack.Text, testStateID) {
		t.Fatalf("ack = %q, want the recipient and the SMSGate message id", ack.Text)
	}
}

// TestHandleReply_UnresolvableTarget asserts a reply to a message the bridge did
// not forward, and a reply to a mapping that has expired, are both answered with
// the friendly text and never queued.
func TestHandleReply_UnresolvableTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		expiresAt    time.Time
		writeMapping bool
	}{
		{
			name:         "unknown message",
			expiresAt:    time.Time{},
			writeMapping: false,
		},
		{
			name:         "expired mapping",
			expiresAt:    time.Now().Add(-time.Hour),
			writeMapping: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newHandlerFixture(t, []int64{testAllowedID}, queued())
			if tt.writeMapping {
				f.storeMapping(t, tt.expiresAt)
			}

			f.deliver(t, updateJSON(testAllowedID, testReplyText))

			waitFor(t, func() bool { return f.telegram.count() == 1 }, "the friendly error reply")

			if got := f.sms.count(); got != 0 {
				t.Fatalf("sms sends = %d, want 0", got)
			}

			ack := f.telegram.last()
			if !strings.Contains(ack.Text, "not a forwarded SMS") {
				t.Fatalf("reply = %q, want the friendly not-forwarded text", ack.Text)
			}

			if strings.Contains(ack.Text, testPhone) {
				t.Fatalf("reply = %q, want no sender number in the text", ack.Text)
			}
		})
	}
}

// TestHandleReply_Allowlist asserts the authorization gate end to end, where it
// now lives: the reply handler builds the allowed-users filter from the config
// it is constructed with, and its route takes the filter while the route is
// registered. An update from a user outside the allowlist is therefore filtered
// out of the route itself: the mapping for that exact bubble is in the store, so
// the SMS would be queued if the update ever reached the service, and the
// handler answers every outcome in chat, so the chat would show a sentence.
// Neither happens, which is what proves the update never entered the handler
// rather than being refused inside it. An empty allowlist closes the route for
// everyone.
func TestHandleReply_Allowlist(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		allowed []int64
		userID  int64
	}{
		{
			name:    "user is not on the allowlist",
			allowed: []int64{testAllowedID},
			userID:  testDeniedID,
		},
		{
			name:    "empty allowlist closes every reply",
			allowed: nil,
			userID:  testAllowedID,
		},
		{
			name:    "empty allowlist closes a route a configured one would open",
			allowed: []int64{},
			userID:  testAllowedID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newHandlerFixture(t, tt.allowed, queued())
			f.storeMapping(t, time.Time{})

			f.deliver(t, updateJSON(tt.userID, testReplyText))

			// Nothing at all is expected, so the wait is a grace period: any
			// output of the handler would show up inside it.
			time.Sleep(graceInterval)

			if got := f.sms.count(); got != 0 {
				t.Fatalf("sms sends = %d, want 0 (the route filters the update)", got)
			}

			if got := f.telegram.count(); got != 0 {
				t.Fatalf("chat messages = %d, want 0 (the route filters the update)", got)
			}
		})
	}
}

// TestHandleReply_SendFailure asserts a refused send is reported in chat without
// leaking the server response, the API credentials or the API address, and that
// the handler stays up for the next update.
func TestHandleReply_SendFailure(t *testing.T) {
	t.Parallel()

	f := newHandlerFixture(t, []int64{testAllowedID}, refused(), queued())
	f.storeMapping(t, time.Time{})

	f.deliver(t, updateJSON(testAllowedID, testReplyText))

	waitFor(t, func() bool { return f.telegram.count() == 1 }, "the failure reply")

	if got := f.sms.count(); got != 1 {
		t.Fatalf("sms sends = %d, want 1", got)
	}

	ack := f.telegram.last()

	if !strings.Contains(ack.Text, "could not be queued") {
		t.Fatalf("reply = %q, want the failure text", ack.Text)
	}

	leaks := []string{
		"401",
		"Unauthorized",
		"api-user",
		"api-password",
		"invalid credentials",
		"sms-gate.internal",
		"http://",
	}

	for _, leak := range leaks {
		if strings.Contains(ack.Text, leak) {
			t.Fatalf("reply = %q, want it free of %q", ack.Text, leak)
		}
	}

	// The bot is still serving updates: the next reply is handled normally.
	f.storeMapping(t, time.Time{})
	f.deliver(t, updateJSON(testAllowedID, "second try"))

	waitFor(t, func() bool { return f.telegram.count() == 2 }, "the answer to the next update")
}

// TestHandleReply_IgnoresOtherMessages asserts the route never queues an SMS for
// traffic that is not an answer to a forwarded bubble, and asks for text when a
// reply carries none.
func TestHandleReply_IgnoresOtherMessages(t *testing.T) {
	t.Parallel()

	t.Run("reply without text is answered with a hint", func(t *testing.T) {
		t.Parallel()

		f := newHandlerFixture(t, []int64{testAllowedID}, queued())
		f.storeMapping(t, time.Time{})

		f.deliver(t, mediaReplyJSON(testAllowedID))

		waitFor(t, func() bool { return f.telegram.count() == 1 }, "the text hint")

		if got := f.sms.count(); got != 0 {
			t.Fatalf("sms sends = %d, want 0", got)
		}

		if ack := f.telegram.last(); !strings.Contains(ack.Text, "with text") {
			t.Fatalf("reply = %q, want the text hint", ack.Text)
		}
	})

	t.Run("plain message is not the handler's business", func(t *testing.T) {
		t.Parallel()

		f := newHandlerFixture(t, []int64{testAllowedID}, queued())
		f.storeMapping(t, time.Time{})

		// The plain message must produce nothing at all. The media reply that
		// follows is the observable event that proves the handler is alive, and
		// it is the only message the chat may end up with.
		f.deliver(t, plainMessageJSON(testAllowedID, "hello"))
		f.deliver(t, mediaReplyJSON(testAllowedID))

		waitFor(t, func() bool { return f.telegram.count() >= 1 }, "the text hint")
		time.Sleep(graceInterval)

		if got := f.sms.count(); got != 0 {
			t.Fatalf("sms sends = %d, want 0", got)
		}

		if got := f.telegram.count(); got != 1 {
			t.Fatalf("chat messages = %d, want 1 (only the hint)", got)
		}
	})
}

// TestForwardThenReply walks the whole slice through the real module
// constructors - boltfx, store, one bridge service used in both directions,
// webhook and the telego adapter - with the two remote services replaced by
// stubs: an SMS arrives at the webhook, is forwarded to Telegram, the mapping is
// written from the id Telegram returned, and an answer to that bubble leaves as
// an SMS to the sender.
//
// Nothing here touches the network: both APIs are httptest servers.
func TestForwardThenReply(t *testing.T) {
	t.Parallel()

	telegram, apiURL := newTelegramServer(t, forwardedMessageID)
	sms := newSMSServer(t, queued())

	repo, _ := newRepo(t)
	bot := newTestBot(t, apiURL)

	sender := smsgate.New(smsgate.Config{
		BaseURL:  sms.url,
		Username: testUsername,
		Password: testPassword,

		Timeout: smsgate.DefaultTimeout,
	}, zap.NewNop())

	service := bridge.New(
		bridge.Config{ChatID: testChatID},
		bot,
		repo,
		sender,
		zap.NewNop(),
	)

	ingest := webhook.New(
		webhook.Config{SigningKey: "test-signing-key"},
		service,
		repo,
		webhook.NewMetricsWithRegisterer(prometheus.NewRegistry()),
		zap.NewNop(),
	)

	// Wave 1 path: the SMSGate app delivers an SMS to the webhook endpoint.
	if ferr := ingest.Forward(context.Background(), testPhone, "delivered SMS"); ferr != nil {
		t.Fatalf("webhook Forward() error = %v", ferr)
	}

	if got := telegram.count(); got != 1 {
		t.Fatalf("forwarded messages = %d, want 1", got)
	}

	// The forward wrote the mapping under the id Telegram reported.
	stored, err := repo.Resolve(context.Background(), testChatID, forwardedMessageID)
	if err != nil {
		t.Fatalf("Resolve() of the forwarded mapping error = %v", err)
	}

	if stored.Phone != testPhone {
		t.Fatalf("mapping phone = %q, want %q", stored.Phone, testPhone)
	}

	// Wave 2 path: the operator answers the forwarded bubble through the same
	// service that forwarded it.
	updates := make(chan telego.Update, 1)
	botHandler, err := th.NewBotHandler(bot.Bot, updates)
	if err != nil {
		t.Fatalf("create bot handler: %v", err)
	}

	replyhandler.New(service, botconfig.Config{AllowedUserIDs: []int64{testAllowedID}}).Register(
		&telegofx.Router{BotHandler: botHandler},
	)

	go func() {
		_ = botHandler.Start()
	}()
	t.Cleanup(func() {
		_ = botHandler.Stop()
	})

	updates <- decodeUpdate(t, updateJSONFor(testAllowedID, testChatID, forwardedMessageID, testReplyText))

	// The acknowledgement is the last step of the handler, so waiting for it
	// also proves the send already happened.
	waitFor(t, func() bool { return telegram.count() == 2 }, "the acknowledgement")

	if got := sms.count(); got != 1 {
		t.Fatalf("sms sends = %d, want 1", got)
	}

	sent := sms.last()
	if len(sent.PhoneNumbers) != 1 || sent.PhoneNumbers[0] != testPhone {
		t.Fatalf("phone numbers = %v, want [%s]", sent.PhoneNumbers, testPhone)
	}

	if sent.TextMessage.Text != testReplyText {
		t.Fatalf("text = %q, want %q", sent.TextMessage.Text, testReplyText)
	}

	// The mapping survives the round trip: it is still resolvable, so a second
	// answer to the same bubble would still be routed.
	if _, rerr := repo.Resolve(context.Background(), testChatID, forwardedMessageID); rerr != nil {
		t.Fatalf("Resolve() after the reply error = %v", rerr)
	}
}

// TestForwardThenReplyExpiredMapping asserts the end of the life of a mapping:
// once the store reports it as expired, the reply stops at the chat and nothing
// is sent, even though the forward itself succeeded.
func TestForwardThenReplyExpiredMapping(t *testing.T) {
	t.Parallel()

	repo, _ := newRepo(t)

	err := repo.Put(context.Background(), testChatID, testTGMessageID, store.Mapping{
		Phone:     testPhone,
		CreatedAt: time.Now().Add(-2 * time.Hour),
		ExpiresAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("store mapping: %v", err)
	}

	f := newHandlerFixture(t, []int64{testAllowedID}, queued())

	f.deliver(t, updateJSON(testAllowedID, testReplyText))

	waitFor(t, func() bool { return f.telegram.count() == 1 }, "the friendly error reply")

	if got := f.sms.count(); got != 0 {
		t.Fatalf("sms sends = %d, want 0", got)
	}
}

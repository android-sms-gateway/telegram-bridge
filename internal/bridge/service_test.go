package bridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"github.com/android-sms-gateway/telegram-bridge/internal/smsgate"
	"github.com/android-sms-gateway/telegram-bridge/internal/store"
	"github.com/go-core-fx/boltfx"
	"github.com/go-core-fx/telegofx"
	"github.com/mymmrac/telego"
	bolt "go.etcd.io/bbolt"
	"go.uber.org/zap"
)

const (
	// testBotToken matches the telego token format ^\d+:[\w-]{35}$ and is never
	// used against the real Telegram API (WithAPIServer redirects all calls).
	testBotToken = "1234567890:AAAAAAAAAABBBBBBBBBBCCCCCCCCCCDDDDD"

	testChatID  int64 = 123456789
	testSender        = "6505551212"
	testMessage       = "hello"

	// telegramMessageLimit mirrors the production per-message character limit.
	telegramMessageLimit = 4096

	// HTML fragments of FormatForwardMessage, used to recover the message part
	// from a formatted chunk.
	forwardPrefixHTML = "New message from <b>"
	forwardSepHTML    = "</b>:\n<code>"
	forwardSuffixHTML = "</code>"
)

// telegramSendRequest mirrors the subset of the Telegram sendMessage body the
// bridge must set.
type telegramSendRequest struct {
	ChatID    int64  `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
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

func TestFormatForwardMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		phoneNumber string
		message     string
		want        string
	}{
		{
			name:        "plain text",
			phoneNumber: testSender,
			message:     "Android is always a sweet treat!",
			want:        "New message from <b>6505551212</b>:\n<code>Android is always a sweet treat!</code>",
		},
		{
			name:        "escapes angle brackets",
			phoneNumber: testSender,
			message:     "<script>alert(1)</script>",
			want:        "New message from <b>6505551212</b>:\n<code>&lt;script&gt;alert(1)&lt;/script&gt;</code>",
		},
		{
			name:        "escapes ampersand",
			phoneNumber: testSender,
			message:     "a&b",
			want:        "New message from <b>6505551212</b>:\n<code>a&amp;b</code>",
		},
		{
			name:        "escapes double quote",
			phoneNumber: testSender,
			message:     `say "hi"`,
			want:        "New message from <b>6505551212</b>:\n<code>say &#34;hi&#34;</code>",
		},
		{
			name:        "escapes single quote",
			phoneNumber: testSender,
			message:     "it's",
			want:        "New message from <b>6505551212</b>:\n<code>it&#39;s</code>",
		},
		{
			name:        "escapes phone number",
			phoneNumber: "<b>@attacker</b>",
			message:     testMessage,
			want:        "New message from <b>&lt;b&gt;@attacker&lt;/b&gt;</b>:\n<code>hello</code>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := bridge.FormatForwardMessage(tt.phoneNumber, tt.message)
			if got != tt.want {
				t.Fatalf("FormatForwardMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestService_Forward(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		chatID        int64
		status        int
		response      string
		wantErr       bool
		wantErrText   string
		wantForwarded []bridge.Forwarded
	}{
		{
			name:        "happy path",
			chatID:      testChatID,
			status:      http.StatusOK,
			response:    `{"ok":true,"result":{"message_id":1}}`,
			wantErr:     false,
			wantErrText: "",
			wantForwarded: []bridge.Forwarded{{
				ChatID:    testChatID,
				MessageID: 1,
			}},
		},
		{
			// The reply path is keyed on the id Telegram assigned, so an id other
			// than the default one must travel back to the caller unchanged.
			name:        "telegram message id is reported",
			chatID:      testChatID,
			status:      http.StatusOK,
			response:    `{"ok":true,"result":{"message_id":4242}}`,
			wantErr:     false,
			wantErrText: "",
			wantForwarded: []bridge.Forwarded{{
				ChatID:    testChatID,
				MessageID: 4242,
			}},
		},
		{
			name:          "telegram error",
			chatID:        testChatID,
			status:        http.StatusBadRequest,
			response:      `{"ok":false,"error_code":400,"description":"Bad Request"}`,
			wantErr:       true,
			wantErrText:   "send telegram message",
			wantForwarded: nil,
		},
		{
			name:          "chat id not configured",
			chatID:        0,
			status:        http.StatusOK,
			response:      `{"ok":true,"result":{"message_id":1}}`,
			wantErr:       true,
			wantErrText:   bridge.ErrNoChatID.Error(),
			wantForwarded: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sent := make(chan telegramSendRequest, 1)
			path := make(chan string, 1)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
				}

				var req telegramSendRequest
				if uerr := json.Unmarshal(body, &req); uerr != nil {
					t.Errorf("unmarshal request body: %v", uerr)
				}

				sent <- req
				path <- r.URL.Path

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()

			// Forward-only construction: the reply collaborators are nil because
			// no case here answers a bubble.
			service := bridge.New(
				bridge.Config{ChatID: tt.chatID},
				newTestBot(t, server.URL),
				nil,
				nil,
				zap.NewNop(),
			)

			forwarded, err := service.Forward(context.Background(), testSender, testMessage)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Forward() error = nil, want error containing %q", tt.wantErrText)
				}

				if !strings.Contains(err.Error(), tt.wantErrText) {
					t.Fatalf("Forward() error = %q, want containing %q", err.Error(), tt.wantErrText)
				}

				if errors.Is(err, bridge.ErrNoChatID) != (tt.chatID == 0) {
					t.Fatalf(
						"Forward() errors.Is(err, ErrNoChatID) = %v, want %v",
						errors.Is(err, bridge.ErrNoChatID),
						tt.chatID == 0,
					)
				}

				if !slices.Equal(forwarded, tt.wantForwarded) {
					t.Fatalf("Forward() = %+v, want %+v on failure", forwarded, tt.wantForwarded)
				}

				return
			}

			if err != nil {
				t.Fatalf("Forward() error = %v", err)
			}

			if !slices.Equal(forwarded, tt.wantForwarded) {
				t.Fatalf("Forward() = %+v, want %+v", forwarded, tt.wantForwarded)
			}

			got := <-sent
			if got.ChatID != testChatID {
				t.Fatalf("chat_id = %d, want %d", got.ChatID, testChatID)
			}

			if got.ParseMode != telego.ModeHTML {
				t.Fatalf("parse_mode = %q, want %q", got.ParseMode, telego.ModeHTML)
			}

			wantText := bridge.FormatForwardMessage(testSender, testMessage)
			if got.Text != wantText {
				t.Fatalf("text = %q, want %q", got.Text, wantText)
			}

			gotPath := <-path
			if wantPath := "/bot" + testBotToken + "/sendMessage"; gotPath != wantPath {
				t.Fatalf("request path = %q, want %q", gotPath, wantPath)
			}
		})
	}
}

// TestForwardReturnsDeliveredPartsOnPartialFailure asserts a failure on a later
// chunk reports the parts Telegram already accepted: the caller stores reply
// mappings for exactly those parts before it reports the error, so a reply to a
// delivered part still resolves to the sender even though the forward as a
// whole failed.
func TestForwardReturnsDeliveredPartsOnPartialFailure(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	requests := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	defer server.Close()

	// Forward-only construction: the reply collaborators are nil because no case
	// here answers a bubble.
	service := bridge.New(
		bridge.Config{ChatID: testChatID},
		newTestBot(t, server.URL),
		nil,
		nil,
		zap.NewNop(),
	)

	// One character over the per-message budget guarantees a second chunk, so
	// the second Telegram call is the one that fails.
	forwarded, err := service.Forward(context.Background(), testSender, strings.Repeat("a", messageBudget()+1))
	if err == nil {
		t.Fatal("Forward() error = nil, want the failure of the second chunk")
	}

	if !strings.Contains(err.Error(), "send telegram message") {
		t.Fatalf("Forward() error = %q, want the send failure", err)
	}

	if len(forwarded) != 1 {
		t.Fatalf("delivered parts = %+v, want exactly the first chunk", forwarded)
	}

	if forwarded[0].ChatID != testChatID || forwarded[0].MessageID != 707 {
		t.Fatalf("delivered part = %+v, want chat %d and message id 707", forwarded[0], testChatID)
	}

	mu.Lock()
	defer mu.Unlock()

	if requests < 2 {
		t.Fatalf("telegram requests = %d, want at least 2 (the first succeeded, the second failed)", requests)
	}
}

// messageBudget is the number of message characters that still fit in one
// Telegram message for testSender, after the fixed prefix and sender.
func messageBudget() int {
	return bridge.MessageBudgetForTest(testSender)
}

// splitFormatted recovers the sender and message parts from a formatted chunk
// for ASCII-only test data (no HTML escaping applies).
func splitFormatted(t *testing.T, chunk string) (string, string) {
	t.Helper()

	if !strings.HasPrefix(chunk, forwardPrefixHTML) || !strings.HasSuffix(chunk, forwardSuffixHTML) {
		t.Fatalf("chunk is not a formatted forward message: %q", chunk)
	}

	inner := chunk[len(forwardPrefixHTML) : len(chunk)-len(forwardSuffixHTML)]

	sender, part, ok := strings.Cut(inner, forwardSepHTML)
	if !ok {
		t.Fatalf("chunk is missing the sender/message separator: %q", chunk)
	}

	return sender, part
}

// assertChunksValid checks that every chunk is a formatted message whose
// sender is testSender and whose parsed length fits Telegram's limit.
func assertChunksValid(t *testing.T, chunks []string, budget int) {
	t.Helper()

	for i, chunk := range chunks {
		sender, part := splitFormatted(t, chunk)

		if sender != testSender {
			t.Fatalf("chunk %d sender = %q, want %q", i, sender, testSender)
		}

		if got := bridge.TextLengthForTest(part); got > budget {
			t.Fatalf("chunk %d message length = %d, want <= %d", i, got, budget)
		}
	}
}

func TestFormatForwardMessageChunks(t *testing.T) {
	t.Parallel()

	budget := messageBudget()

	tests := []struct {
		name      string
		sender    string
		message   string
		wantParts int
		wantErr   error
	}{
		{
			name:      "short message stays a single part",
			sender:    testSender,
			message:   testMessage,
			wantParts: 1,
			wantErr:   nil,
		},
		{
			name:      "exact budget stays a single part",
			sender:    testSender,
			message:   strings.Repeat("a", budget),
			wantParts: 1,
			wantErr:   nil,
		},
		{
			name:      "one over budget splits in two",
			sender:    testSender,
			message:   strings.Repeat("a", budget+1),
			wantParts: 2,
			wantErr:   nil,
		},
		{
			name:      "multiple parts",
			sender:    testSender,
			message:   strings.Repeat("a", budget*3+7),
			wantParts: 4,
			wantErr:   nil,
		},
		{
			// Each emoji is two UTF-16 code units, so only budget/2 fit per
			// part; the rune itself is never cut in half.
			name:      "astral runes are not split",
			sender:    testSender,
			message:   strings.Repeat("\U0001F600", budget),
			wantParts: 3,
			wantErr:   nil,
		},
		{
			name:      "sender leaves no room",
			sender:    strings.Repeat("9", telegramMessageLimit),
			message:   testMessage,
			wantParts: 0,
			wantErr:   bridge.ErrMessageTooLong,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			chunks, err := bridge.FormatForwardMessageChunksForTest(tt.sender, tt.message)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("error = %v", err)
			}

			if len(chunks) != tt.wantParts {
				t.Fatalf("parts = %d, want %d", len(chunks), tt.wantParts)
			}

			assertChunksValid(t, chunks, budget)

			var rebuilt strings.Builder
			for _, chunk := range chunks {
				_, part := splitFormatted(t, chunk)
				rebuilt.WriteString(part)
			}

			if rebuilt.String() != tt.message {
				t.Fatalf("rebuilt message = %q, want %q", rebuilt.String(), tt.message)
			}
		})
	}
}

func TestService_ForwardSplitsLongMessage(t *testing.T) {
	t.Parallel()

	var (
		mu    sync.Mutex
		texts []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req telegramSendRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request body: %v", err)
		}

		mu.Lock()
		texts = append(texts, req.Text)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer server.Close()

	service := bridge.New(bridge.Config{ChatID: testChatID}, newTestBot(t, server.URL), nil, nil, zap.NewNop())

	message := strings.Repeat("x", messageBudget()*2+5)

	forwarded, err := service.Forward(context.Background(), testSender, message)
	if err != nil {
		t.Fatalf("Forward() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(texts) < 2 {
		t.Fatalf("sendMessage calls = %d, want at least 2", len(texts))
	}

	// Every part Telegram accepted must be reported back, otherwise a reply to
	// any earlier part of a split message would never resolve to a sender.
	if len(forwarded) != len(texts) {
		t.Fatalf("Forward() reported %d parts, want %d", len(forwarded), len(texts))
	}

	assertChunksValid(t, texts, messageBudget())

	var rebuilt strings.Builder
	for _, text := range texts {
		_, part := splitFormatted(t, text)
		rebuilt.WriteString(part)
	}

	if rebuilt.String() != message {
		t.Fatalf("rebuilt message = %q, want %q", rebuilt.String(), message)
	}
}

const (
	testAllowedID   int64 = 111
	testDeniedID    int64 = 222
	testTGMessageID       = 4242
	testPhone             = "6505551212"
	testReplyText         = "call me back"
	testStateID           = "sms-1"

	testUsername = "api-user"
	testPassword = "api-password"
)

// smsSend mirrors the subset of the SMSGate send body the service must produce.
type smsSend struct {
	ID           string   `json:"id"`
	PhoneNumbers []string `json:"phoneNumbers"`
	TextMessage  struct {
		Text string `json:"text"`
	} `json:"textMessage"`
}

// smsResponse is one scripted answer of the SMSGate server stub.
type smsResponse struct {
	status int
	body   string
}

// queued, conflict and refused are the SMSGate server answers the cases need: an
// accepted message carrying its id, a conflict that carries none, and a refusal
// whose text names the account and the server address.
func queued() smsResponse {
	return smsResponse{status: http.StatusOK, body: `{"id":"` + testStateID + `","state":"Pending"}`}
}

func conflict() smsResponse {
	return smsResponse{status: http.StatusConflict, body: `{"error":"message already exists"}`}
}

func refused() smsResponse {
	return smsResponse{
		status: http.StatusUnauthorized,
		body:   `{"error":"invalid credentials for api-user at https://sms-gate.internal"}`,
	}
}

// smsStub stands in for the SMSGate server API and records every send.
type smsStub struct {
	url       string
	responses []smsResponse

	mu   sync.Mutex
	sent []smsSend
}

// newSMSServer starts the SMSGate server stub that answers the i-th send with
// responses[i] (the last response repeats once the sequence is exhausted).
func newSMSServer(t *testing.T, responses ...smsResponse) *smsStub {
	t.Helper()

	if len(responses) == 0 {
		t.Fatal("newSMSServer() needs at least one scripted response")
	}

	sms := &smsStub{
		url:       "",
		responses: responses,

		mu:   sync.Mutex{},
		sent: nil,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read smsgate request body: %v", err)
		}

		var send smsSend
		if uerr := json.Unmarshal(raw, &send); uerr != nil {
			t.Errorf("unmarshal smsgate request body: %v", uerr)
		}

		sms.mu.Lock()
		response := sms.responses[min(len(sms.responses)-1, len(sms.sent))]
		sms.sent = append(sms.sent, send)
		sms.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(response.status)
		_, _ = w.Write([]byte(response.body))
	}))
	t.Cleanup(server.Close)

	sms.url = server.URL

	return sms
}

// count returns how many sends the stub received.
func (s *smsStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.sent)
}

// last returns the most recent send, or an empty one when nothing was sent.
func (s *smsStub) last() smsSend {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.sent) == 0 {
		return smsSend{}
	}

	return s.sent[len(s.sent)-1]
}

// newRepo opens the mapping repository in a temporary directory. The raw handle
// is returned so a case can close the database and force a lookup failure.
func newRepo(t *testing.T) (*store.Repository, *bolt.DB) {
	t.Helper()

	db, err := boltfx.New(boltfx.Config{URL: "bolt://" + filepath.Join(t.TempDir(), "reply.db")}, zap.NewNop())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	// A second Close on a bbolt handle is a no-op, so a case that closed the
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

// fixture bundles the bridge service with the two collaborators it owns: the
// durable mapping store and the SMSGate server API.
type fixture struct {
	service *bridge.Service

	repo *store.Repository
	db   *bolt.DB

	sms *smsStub
}

// newFixture wires a bridge service against a stub SMSGate server, exactly as the
// serve graph does: the same store service the forward path writes, and the
// same smsgate service.
func newFixture(t *testing.T, responses ...smsResponse) *fixture {
	t.Helper()

	repo, db := newRepo(t)
	sms := newSMSServer(t, responses...)

	// The forward half of the service is not exercised here, so it is built
	// without a bot: every case below only answers a forwarded bubble.
	service := bridge.New(
		bridge.Config{ChatID: 0},
		nil,
		store.New(repo, zap.NewNop()),
		smsgate.New(smsgate.Config{
			BaseURL:  sms.url,
			Username: testUsername,
			Password: testPassword,

			Timeout: smsgate.DefaultTimeout,
		}, zap.NewNop()),
		zap.NewNop(),
	)

	return &fixture{
		service: service,

		repo: repo,
		db:   db,

		sms: sms,
	}
}

// storeMapping writes the mapping a forward would have written. The lifetime
// is the store's business: the repository stamps it from its TTL, so the
// fixture writes the domain record like every production caller does.
func (f *fixture) storeMapping(t *testing.T) {
	t.Helper()

	err := f.repo.Put(context.Background(), store.Mapping{
		MessageRef: store.MessageRef{ChatID: testChatID, MessageID: testTGMessageID},
		Phone:      testPhone,
	})
	if err != nil {
		t.Fatalf("store mapping: %v", err)
	}
}

// answer is the reply the service is asked about: an allowed Telegram user
// answering the forwarded bubble in the test chat. The sender is a fixed value
// because authorization is not the service's business any more: the bot module
// filters updates by sender before they reach this service.
func answer(text string) bridge.Answer {
	return bridge.Answer{
		UserID:           testAllowedID,
		ChatID:           testChatID,
		ReplyToMessageID: testTGMessageID,
		Text:             text,
	}
}

// TestReply_SendsToMappedSender covers the round trip of a single answer: an
// allowed user answers a forwarded bubble, the answer leaves for the mapped
// sender, and the outcome carries the id the SMSGate server assigned.
func TestReply_SendsToMappedSender(t *testing.T) {
	t.Parallel()

	f := newFixture(t, queued())
	f.storeMapping(t)

	got := f.service.Reply(context.Background(), answer(testReplyText))

	want := bridge.Outcome{Kind: bridge.OutcomeSent, Phone: testPhone, MessageID: testStateID}
	if got != want {
		t.Fatalf("Reply() outcome = %+v, want %+v", got, want)
	}

	if n := f.sms.count(); n != 1 {
		t.Fatalf("sms sends = %d, want 1", n)
	}

	send := f.sms.last()
	if len(send.PhoneNumbers) != 1 || send.PhoneNumbers[0] != testPhone {
		t.Fatalf("phone numbers = %v, want [%s]", send.PhoneNumbers, testPhone)
	}

	if send.TextMessage.Text != testReplyText {
		t.Fatalf("text = %q, want %q", send.TextMessage.Text, testReplyText)
	}
}

// TestReply_ConflictCarriesQueuedID asserts a conflict is still reported as
// queued and the outcome carries the id the message was queued under: the
// SMSGate conflict answer itself carries no id, so the chat shows the stable id
// the send was generated with, never an empty one or the placeholder.
func TestReply_ConflictCarriesQueuedID(t *testing.T) {
	t.Parallel()

	f := newFixture(t, conflict())
	f.storeMapping(t)

	got := f.service.Reply(context.Background(), answer(testReplyText))

	if got.Kind != bridge.OutcomeSent || got.Phone != testPhone {
		t.Fatalf("Reply() outcome = %+v, want queued for %s", got, testPhone)
	}

	send := f.sms.last()
	if send.ID == "" {
		t.Fatal("queued request id = empty, want a client-generated id")
	}

	if got.MessageID != send.ID {
		t.Fatalf("outcome message id = %q, want the queued id %q", got.MessageID, send.ID)
	}

	if n := f.sms.count(); n != 1 {
		t.Fatalf("sms sends = %d, want 1", n)
	}
}

// TestReply_EmptyText asserts a reply that carries no text (a photo, for example)
// never leaves as an SMS.
func TestReply_EmptyText(t *testing.T) {
	t.Parallel()

	f := newFixture(t, queued())
	f.storeMapping(t)

	got := f.service.Reply(context.Background(), answer(""))

	want := bridge.Outcome{Kind: bridge.OutcomeEmptyText, Phone: "", MessageID: ""}
	if got != want {
		t.Fatalf("Reply() outcome = %+v, want %+v", got, want)
	}

	if n := f.sms.count(); n != 0 {
		t.Fatalf("sms sends = %d, want 0", n)
	}
}

// TestReply_UnresolvableTarget asserts a reply to a message the bridge did not
// forward is reported as an unroutable target and never queued. An expired
// mapping reaches the same outcome through the store's ErrMappingNotFound,
// which the store's own tests pin.
func TestReply_UnresolvableTarget(t *testing.T) {
	t.Parallel()

	f := newFixture(t, queued())

	got := f.service.Reply(context.Background(), answer(testReplyText))

	want := bridge.Outcome{Kind: bridge.OutcomeNotForwarded, Phone: "", MessageID: ""}
	if got != want {
		t.Fatalf("Reply() outcome = %+v, want %+v", got, want)
	}

	if n := f.sms.count(); n != 0 {
		t.Fatalf("sms sends = %d, want 0", n)
	}
}

// TestReply_LookupFailed asserts a mapping lookup that fails for any reason other
// than a missing record is reported as its own outcome: the operator is told the
// lookup failed, and nothing about the store reaches the outcome.
func TestReply_LookupFailed(t *testing.T) {
	t.Parallel()

	f := newFixture(t, queued())
	f.storeMapping(t)

	// Closing the handle makes every later read fail with ErrDatabaseNotOpen.
	if err := f.db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	got := f.service.Reply(context.Background(), answer(testReplyText))

	want := bridge.Outcome{Kind: bridge.OutcomeLookupFailed, Phone: "", MessageID: ""}
	if got != want {
		t.Fatalf("Reply() outcome = %+v, want %+v", got, want)
	}

	if n := f.sms.count(); n != 0 {
		t.Fatalf("sms sends = %d, want 0", n)
	}
}

// TestReply_SendFailure asserts a refused send is reported as a send failure that
// carries neither the sender number nor a server answer, and that the service
// stays usable for the next reply.
func TestReply_SendFailure(t *testing.T) {
	t.Parallel()

	f := newFixture(t, refused(), queued())
	f.storeMapping(t)

	got := f.service.Reply(context.Background(), answer(testReplyText))

	want := bridge.Outcome{Kind: bridge.OutcomeSendFailed, Phone: "", MessageID: ""}
	if got != want {
		t.Fatalf("Reply() outcome = %+v, want %+v", got, want)
	}

	if n := f.sms.count(); n != 1 {
		t.Fatalf("sms sends = %d, want 1", n)
	}

	// The service stays up: the next reply is handled normally.
	f.storeMapping(t)

	next := f.service.Reply(context.Background(), answer("second try"))

	wantNext := bridge.Outcome{Kind: bridge.OutcomeSent, Phone: testPhone, MessageID: testStateID}
	if next != wantNext {
		t.Fatalf("second Reply() outcome = %+v, want %+v", next, wantNext)
	}
}

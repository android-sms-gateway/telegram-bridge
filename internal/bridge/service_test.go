package bridge_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
	"github.com/go-core-fx/telegofx"
	"github.com/mymmrac/telego"
	"go.uber.org/zap"
)

const (
	// testBotToken matches the telego token format ^\d+:[\w-]{35}$ and is never
	// used against the real Telegram API (WithAPIServer redirects all calls).
	testBotToken = "1234567890:AAAAAAAAAABBBBBBBBBBCCCCCCCCCCDDDDD"

	testChatID  int64 = 123456789
	testSender        = "6505551212"
	testMessage       = "hello"
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
		name        string
		chatID      int64
		status      int
		response    string
		wantErr     bool
		wantErrText string
	}{
		{
			name:        "happy path",
			chatID:      testChatID,
			status:      http.StatusOK,
			response:    `{"ok":true,"result":{"message_id":1}}`,
			wantErr:     false,
			wantErrText: "",
		},
		{
			name:        "telegram error",
			chatID:      testChatID,
			status:      http.StatusBadRequest,
			response:    `{"ok":false,"error_code":400,"description":"Bad Request"}`,
			wantErr:     true,
			wantErrText: "send telegram message",
		},
		{
			name:        "chat id not configured",
			chatID:      0,
			status:      http.StatusOK,
			response:    `{"ok":true,"result":{"message_id":1}}`,
			wantErr:     true,
			wantErrText: bridge.ErrNoChatID.Error(),
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

			service := bridge.New(bridge.Config{ChatID: tt.chatID}, newTestBot(t, server.URL), zap.NewNop())

			err := service.Forward(context.Background(), testSender, testMessage)
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

				return
			}

			if err != nil {
				t.Fatalf("Forward() error = %v", err)
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

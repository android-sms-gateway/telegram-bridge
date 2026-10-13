package smsgate_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/android-sms-gateway/client-go/rest"
	"github.com/android-sms-gateway/telegram-bridge/internal/smsgate"
	"go.uber.org/zap"
)

const (
	testUsername = "api-user"
	testPassword = "api-password"
	testPhone    = "6505551212"
	testText     = "answer from telegram"
	testStateID  = "msg-42"
)

// sendRequest mirrors the subset of the SMSGate send body the bridge must set.
type sendRequest struct {
	PhoneNumbers []string `json:"phoneNumbers"`
	TextMessage  struct {
		Text string `json:"text"`
	} `json:"textMessage"`
	Priority int `json:"priority"`
}

// stub is a stand-in for the SMSGate server API. It records every request it
// receives and answers with a scripted sequence of responses.
type stub struct {
	server *httptest.Server

	mu       sync.Mutex
	requests []sendRequest
	auth     []string
	statuses []int
}

// newStub starts a server that answers the i-th request with statuses[i]
// (the last status repeats once the sequence is exhausted).
func newStub(t *testing.T, statuses ...int) *stub {
	t.Helper()

	if len(statuses) == 0 {
		t.Fatal("newStub() needs at least one response status")
	}

	s := &stub{
		server:   nil,
		mu:       sync.Mutex{},
		requests: nil,
		auth:     nil,
		statuses: statuses,
	}

	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}

		var req sendRequest
		if uerr := json.Unmarshal(body, &req); uerr != nil {
			t.Errorf("unmarshal request body: %v", uerr)
		}

		s.mu.Lock()
		status := s.statuses[min(len(s.statuses)-1, len(s.requests))]
		s.requests = append(s.requests, req)
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)

		switch status {
		case http.StatusOK:
			_, _ = w.Write([]byte(`{"id":"` + testStateID + `","state":"Pending"}`))
		case http.StatusConflict:
			_, _ = w.Write([]byte(`{"error":"message already exists"}`))
		default:
			_, _ = w.Write([]byte(`{"error":"stub failure"}`))
		}
	}))
	t.Cleanup(s.server.Close)

	return s
}

// calls returns how many requests the stub has served.
func (s *stub) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.requests)
}

// call returns the i-th recorded request with its authorization header.
func (s *stub) call(t *testing.T, i int) (sendRequest, string) {
	t.Helper()

	s.mu.Lock()
	defer s.mu.Unlock()

	if i >= len(s.requests) {
		t.Fatalf("stub served %d requests, want at least %d", len(s.requests), i+1)
	}

	return s.requests[i], s.auth[i]
}

// newService builds a service pointed at the stub with the given timeout.
func newService(t *testing.T, baseURL string, timeout time.Duration) *smsgate.Service {
	t.Helper()

	return smsgate.New(smsgate.Config{
		BaseURL:  baseURL,
		Username: testUsername,
		Password: testPassword,

		Timeout: timeout,
	}, zap.NewNop())
}

// wantBasicAuth is the Authorization header client-go builds from the
// configured credentials.
func wantBasicAuth() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(testUsername+":"+testPassword))
}

func TestService_Send(t *testing.T) {
	t.Parallel()

	server := newStub(t, http.StatusOK)
	service := newService(t, server.server.URL, smsgate.DefaultTimeout)

	id, err := service.Send(context.Background(), testPhone, testText)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if id != testStateID {
		t.Fatalf("Send() id = %q, want %q", id, testStateID)
	}

	req, auth := server.call(t, 0)
	if auth != wantBasicAuth() {
		t.Fatalf("Authorization header = %q, want %q", auth, wantBasicAuth())
	}

	if len(req.PhoneNumbers) != 1 || req.PhoneNumbers[0] != testPhone {
		t.Fatalf("phone numbers = %v, want [%s]", req.PhoneNumbers, testPhone)
	}

	if req.TextMessage.Text != testText {
		t.Fatalf("text = %q, want %q", req.TextMessage.Text, testText)
	}
}

// TestService_SendRetriesServerError asserts a 5xx is retried and the send still
// succeeds once the server recovers.
func TestService_SendRetriesServerError(t *testing.T) {
	t.Parallel()

	server := newStub(t, http.StatusServiceUnavailable, http.StatusOK)
	service := newService(t, server.server.URL, smsgate.DefaultTimeout)

	id, err := service.Send(context.Background(), testPhone, testText)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if id != testStateID {
		t.Fatalf("Send() id = %q, want %q", id, testStateID)
	}

	if got := server.calls(); got != 2 {
		t.Fatalf("requests served = %d, want 2 (one failure and one retry)", got)
	}
}

// TestService_SendClassifiesErrors pins the client-go error classification the
// retry loop depends on: a client error is permanent, a conflict is a success
// and a server error is retried.
func TestService_SendClassifiesErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		statuses  []int
		wantErr   bool
		wantIs    func(error) bool
		wantID    string
		wantCalls int
	}{
		{
			name:      "bad request is permanent",
			statuses:  []int{http.StatusBadRequest, http.StatusOK},
			wantErr:   true,
			wantIs:    rest.IsBadRequest,
			wantID:    "",
			wantCalls: 1,
		},
		{
			name:      "unauthorized is a client error",
			statuses:  []int{http.StatusUnauthorized, http.StatusOK},
			wantErr:   true,
			wantIs:    rest.IsClientError,
			wantID:    "",
			wantCalls: 1,
		},
		{
			name:      "conflict means already enqueued",
			statuses:  []int{http.StatusConflict},
			wantErr:   false,
			wantIs:    nil,
			wantID:    "",
			wantCalls: 1,
		},
		{
			// The server error is retried, the client error that follows is not:
			// the call count tells the two apart without spending the whole retry
			// budget of the module.
			name:      "server error is retried, the next client error is not",
			statuses:  []int{http.StatusInternalServerError, http.StatusBadRequest, http.StatusOK},
			wantErr:   true,
			wantIs:    rest.IsBadRequest,
			wantID:    "",
			wantCalls: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := newStub(t, tt.statuses...)
			service := newService(t, server.server.URL, smsgate.DefaultTimeout)

			id, err := service.Send(context.Background(), testPhone, testText)

			if (err != nil) != tt.wantErr {
				t.Fatalf("Send() error = %v, want error: %v", err, tt.wantErr)
			}

			if id != tt.wantID {
				t.Fatalf("Send() id = %q, want %q", id, tt.wantID)
			}

			if tt.wantErr && tt.wantIs != nil && !tt.wantIs(err) {
				t.Fatalf("Send() error = %v, want it classified by the test predicate", err)
			}

			if calls := server.calls(); calls != tt.wantCalls {
				t.Fatalf("requests served = %d, want exactly %d", calls, tt.wantCalls)
			}
		})
	}
}

// TestService_SendWithoutCredentials asserts the service fails closed before it
// issues a request when no API credentials are configured.
func TestService_SendWithoutCredentials(t *testing.T) {
	t.Parallel()

	server := newStub(t, http.StatusOK)
	service := smsgate.New(smsgate.Config{
		BaseURL:  server.server.URL,
		Username: "",
		Password: "",

		Timeout: smsgate.DefaultTimeout,
	}, zap.NewNop())

	id, err := service.Send(context.Background(), testPhone, testText)
	if !errors.Is(err, smsgate.ErrNoCredentials) {
		t.Fatalf("Send() error = %v, want ErrNoCredentials", err)
	}

	if id != "" {
		t.Fatalf("Send() id = %q, want empty", id)
	}

	if calls := server.calls(); calls != 0 {
		t.Fatalf("requests served = %d, want 0", calls)
	}
}

// TestService_SendHonorsContext asserts a cancelled context stops the retry loop
// instead of hammering a server the caller already gave up on.
func TestService_SendHonorsContext(t *testing.T) {
	t.Parallel()

	server := newStub(t, http.StatusServiceUnavailable)
	service := newService(t, server.server.URL, smsgate.DefaultTimeout)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := service.Send(ctx, testPhone, testText); !errors.Is(err, context.Canceled) {
		t.Fatalf("Send() error = %v, want context.Canceled", err)
	}

	if calls := server.calls(); calls > 1 {
		t.Fatalf("requests served = %d, want at most 1 after cancellation", calls)
	}
}

// TestService_SendDefaultTimeout asserts a config without a timeout still sends:
// the module default is applied instead of an immediate deadline.
func TestService_SendDefaultTimeout(t *testing.T) {
	t.Parallel()

	server := newStub(t, http.StatusOK)
	service := newService(t, server.server.URL, 0)

	if _, err := service.Send(context.Background(), testPhone, testText); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	if calls := server.calls(); calls != 1 {
		t.Fatalf("requests served = %d, want 1", calls)
	}
}

package reply_test

import (
	"strings"
	"testing"

	"github.com/android-sms-gateway/telegram-bridge/internal/bot/handlers/reply"
	"github.com/android-sms-gateway/telegram-bridge/internal/bridge"
)

const (
	testPhone   = "6505551212"
	testStateID = "sms-1"
)

// TestOutcomeText pins every sentence the operator can read. The bridge service
// reports an outcome, never a message, so this is the only place the wording
// lives: a renamed field of the outcome must not be able to change it silently.
// There is no sentence for a refused user on purpose: an unauthorized update is
// filtered out of the route by the allowed-users filter and never reaches this
// handler, so the chat is not answered at all.
func TestOutcomeText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		outcome bridge.Outcome
		want    string
	}{
		{
			name:    "reply without text",
			outcome: bridge.Outcome{Kind: bridge.OutcomeEmptyText, Phone: "", MessageID: ""},
			want:    "Reply to a forwarded message with text and it will be sent as an SMS.",
		},
		{
			name:    "message the bridge never forwarded",
			outcome: bridge.Outcome{Kind: bridge.OutcomeNotForwarded, Phone: "", MessageID: ""},
			want: "That message is not a forwarded SMS anymore, so I cannot tell " +
				"who to reply to.",
		},
		{
			name:    "mapping lookup failed",
			outcome: bridge.Outcome{Kind: bridge.OutcomeLookupFailed, Phone: "", MessageID: ""},
			want:    "I could not look up the sender of that message. Please check the bridge logs.",
		},
		{
			name:    "send refused by the server",
			outcome: bridge.Outcome{Kind: bridge.OutcomeSendFailed, Phone: "", MessageID: ""},
			want:    "The SMS could not be queued. Please check the bridge logs.",
		},
		{
			name: "queued send is acknowledged with recipient and id",
			outcome: bridge.Outcome{
				Kind:      bridge.OutcomeSent,
				Phone:     testPhone,
				MessageID: testStateID,
			},
			want: "Sent to 6505551212 (SMSGate message id: sms-1).",
		},
		{
			name: "queued send reports the placeholder id the service substituted",
			outcome: bridge.Outcome{
				Kind:      bridge.OutcomeSent,
				Phone:     testPhone,
				MessageID: "unknown",
			},
			want: "Sent to 6505551212 (SMSGate message id: unknown).",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := reply.OutcomeText(tt.outcome); got != tt.want {
				t.Fatalf("OutcomeText(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

// TestOutcomeTextLeaksNothing asserts that a failed outcome renders a sentence
// carrying nothing but the operator's own action: no status code, no API
// account, no server answer and no server address. The outcome itself already
// refuses to carry them, so this pins the wording end of the chain as well.
func TestOutcomeTextLeaksNothing(t *testing.T) {
	t.Parallel()

	failures := []bridge.Outcome{
		{Kind: bridge.OutcomeEmptyText, Phone: "", MessageID: ""},
		{Kind: bridge.OutcomeNotForwarded, Phone: "", MessageID: ""},
		{Kind: bridge.OutcomeLookupFailed, Phone: "", MessageID: ""},
		{Kind: bridge.OutcomeSendFailed, Phone: "", MessageID: ""},
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

	for _, outcome := range failures {
		text := reply.OutcomeText(outcome)

		for _, leak := range leaks {
			if strings.Contains(text, leak) {
				t.Fatalf("OutcomeText(kind %d) = %q, want it free of %q", outcome.Kind, text, leak)
			}
		}

		if strings.Contains(text, testPhone) {
			t.Fatalf("OutcomeText(kind %d) = %q, want no sender number in a failure", outcome.Kind, text)
		}
	}
}

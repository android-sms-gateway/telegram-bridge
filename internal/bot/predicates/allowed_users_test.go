package predicates_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/android-sms-gateway/telegram-bridge/internal/bot/predicates"
	"github.com/mymmrac/telego"
)

const (
	testAllowedUserID int64 = 111
	testOtherUserID   int64 = 222
	testChatID        int64 = 123456789
)

// messageUpdate renders a message update written by userID in chatID.
func messageUpdate(userID, chatID int64) string {
	return fmt.Sprintf(`{"update_id":1,"message":{"message_id":11,`+
		`"from":{"id":%d,"is_bot":false,"first_name":"Operator"},`+
		`"chat":{"id":%d,"type":"private"},"text":"call me back"}}`,
		userID, chatID)
}

// decodeUpdate turns a raw Telegram update body into an update value, the same
// way Telegram hands one to the bot.
func decodeUpdate(t *testing.T, raw string) telego.Update {
	t.Helper()

	var update telego.Update
	if err := json.Unmarshal([]byte(raw), &update); err != nil {
		t.Fatalf("unmarshal update: %v", err)
	}

	return update
}

// TestAllowedUsersMatch pins who reaches a route that takes the filter: the
// configured Telegram users, and nobody else. An update with no message or no
// sender matches nothing, because there is no actor to authorize.
func TestAllowedUsersMatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		allowed []int64
		raw     string
		want    bool
	}{
		{
			name:    "an allowed user matches",
			allowed: []int64{testAllowedUserID},
			raw:     messageUpdate(testAllowedUserID, testChatID),
			want:    true,
		},
		{
			name:    "every allowed user matches",
			allowed: []int64{testAllowedUserID, testOtherUserID},
			raw:     messageUpdate(testOtherUserID, testChatID),
			want:    true,
		},
		{
			name:    "a repeated id still matches",
			allowed: []int64{testAllowedUserID, testAllowedUserID},
			raw:     messageUpdate(testAllowedUserID, testChatID),
			want:    true,
		},
		{
			name:    "a user outside the allowlist does not match",
			allowed: []int64{testAllowedUserID},
			raw:     messageUpdate(testOtherUserID, testChatID),
			want:    false,
		},
		{
			name:    "an allowed user matches in any chat",
			allowed: []int64{testAllowedUserID},
			raw:     messageUpdate(testAllowedUserID, testOtherUserID),
			want:    true,
		},
		{
			name:    "a nil allowlist matches nothing",
			allowed: nil,
			raw:     messageUpdate(testAllowedUserID, testChatID),
			want:    false,
		},
		{
			name:    "an empty allowlist matches nothing",
			allowed: []int64{},
			raw:     messageUpdate(testAllowedUserID, testChatID),
			want:    false,
		},
		{
			name:    "an update without a message does not match",
			allowed: []int64{testAllowedUserID},
			raw:     `{"update_id":1}`,
			want:    false,
		},
		{
			name:    "a message without a sender does not match",
			allowed: []int64{testAllowedUserID},
			raw: `{"update_id":1,"message":{"message_id":11,` +
				`"chat":{"id":123456789,"type":"private"},"text":"call me back"}}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			allowed := predicates.NewAllowedUsers(tt.allowed)

			got := allowed.Match(context.Background(), decodeUpdate(t, tt.raw))
			if got != tt.want {
				t.Fatalf("Match() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestAllowedUsersZeroValueMatchesNothing asserts the fail-closed default of the
// type itself: a matcher nobody configured matches nothing, so a route that
// receives one is closed rather than open.
func TestAllowedUsersZeroValueMatchesNothing(t *testing.T) {
	t.Parallel()

	var allowed predicates.AllowedUsers

	if allowed.Match(context.Background(), decodeUpdate(t, messageUpdate(testAllowedUserID, testChatID))) {
		t.Fatal("Match() = true for the zero value, want false (fail-closed)")
	}
}

// TestMatchDoesNotMutateUpdate asserts the filter only reads: an update handed
// to it comes out byte-identical, so the next route in the chain sees what
// Telegram sent.
func TestMatchDoesNotMutateUpdate(t *testing.T) {
	t.Parallel()

	update := decodeUpdate(t, messageUpdate(testAllowedUserID, testChatID))

	before, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal update before Match(): %v", err)
	}

	allowed := predicates.NewAllowedUsers([]int64{testAllowedUserID})

	if !allowed.Match(context.Background(), update) {
		t.Fatal("Match() = false, want true for the allowed sender")
	}

	after, err := json.Marshal(update)
	if err != nil {
		t.Fatalf("marshal update after Match(): %v", err)
	}

	if !bytes.Equal(before, after) {
		t.Fatalf("update changed by Match():\nbefore %s\nafter  %s", before, after)
	}
}

// Package bridge is the outbound half of the SMSGate -> Telegram bridge.
//
// It renders incoming SMSGate messages as HTML and delivers them to the
// configured chat via the telegofx-provided bot. The bot itself (token,
// long polling, update routing) is owned by telegofx; this package only sends,
// and owns the forward target taken from the raw bridge.chat_id setting.
package bridge

import "errors"

var (
	// ErrNoChatID is returned when the forward target chat is not configured.
	ErrNoChatID = errors.New("bridge: chat id is not configured")

	// ErrMessageTooLong is returned when the sender and the fixed prefix leave
	// no room for the SMS text inside Telegram's per-message limit.
	ErrMessageTooLong = errors.New("bridge: sender leaves no room for the message")
)

// Package bridge is the bidirectional SMSGate <-> Telegram bridge.
//
// Forward (SMSGate -> Telegram): it renders an incoming SMSGate message as HTML
// and delivers it to the configured chat via the telegofx-provided bot, then
// reports the chat and the message id Telegram assigned, which is exactly the
// pair the reply path resolves back through the durable store.
//
// Reply (Telegram -> SMSGate): it takes an answer typed in Telegram back to the
// SMS sender, resolving the replied-to message through internal/store and
// handing the text to internal/smsgate.
//
// The bot itself (token, long polling, update routing) is owned by telegofx;
// this package only sends. Its configuration is the single forward target read
// from bridge.chat_id: who may talk to the bot is decided by internal/bot, which
// filters updates at the route and never hands an unauthorized one here.
package bridge

import "errors"

var (
	// ErrNoChatID is returned when the forward target chat is not configured.
	ErrNoChatID = errors.New("bridge: chat id is not configured")
)

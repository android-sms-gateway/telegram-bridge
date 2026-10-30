package bridge

// OutcomeKind classifies the result of one Reply.
type OutcomeKind int

// Forwarded identifies a message the bridge delivered to Telegram.
//
// Both halves are reported by the delivery itself, so a caller that records the
// forward can never pair a Telegram message id with a chat other than the one it
// was actually sent to.
type Forwarded struct {
	// ChatID is the chat the message was delivered to.
	ChatID int64

	// MessageID is the id Telegram assigned to the delivered message. A reply to
	// the forwarded bubble carries it back to the bot, which is what makes the
	// reply path resolvable.
	MessageID int
}

// Answer is one Telegram message that replies to a forwarded bubble.
type Answer struct {
	// UserID is the Telegram user who wrote the reply. It is the identity of the
	// sender, not an authorization input: the reply route of internal/bot
	// already admitted the update through the allowed-users filter while it was
	// registered, so this service never has to decide who is allowed to reply.
	UserID int64

	// ChatID is the chat the reply was written in and the one half of the
	// mapping key.
	ChatID int64

	// ReplyToMessageID is the id of the forwarded message being answered, the
	// other half of the mapping key.
	ReplyToMessageID int

	// Text is what the operator typed. Empty means the reply carried no text.
	Text string
}

// Outcome is the result of one Reply.
//
// Phone and MessageID are set only for OutcomeSent: a failed outcome carries
// nothing that could be mistaken for a detail of the internal failure.
type Outcome struct {
	// Kind is what happened.
	Kind OutcomeKind

	// Phone is the SMS sender the answer was queued for. Set on OutcomeSent only.
	Phone string

	// MessageID is the id the SMSGate server assigned to the queued message, or
	// the "unknown" placeholder when it answered without one. Set on
	// OutcomeSent only.
	MessageID string
}

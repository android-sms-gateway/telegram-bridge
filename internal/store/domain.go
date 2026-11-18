package store

import "fmt"

// MessageRef identifies a forwarded Telegram message: the chat and message pair
// a reply quotes, and the identity the durable record is keyed by.
type MessageRef struct {
	ChatID    int64
	MessageID int
}

// key is the mappings bucket key of the referenced message. A Telegram chat id
// is a signed integer and never contains the separator, so the joined key is
// unambiguous.
func (r MessageRef) key() string {
	return fmt.Sprintf("%d:%d", r.ChatID, r.MessageID)
}

// Mapping is the durable record that resolves a Telegram reply back to the SMS
// sender it was forwarded from. It carries the identity of the forwarded
// message through the embedded MessageRef.
type Mapping struct {
	MessageRef

	// Phone is the SMS sender number the message was forwarded from, exactly as
	// the SMSGate webhook reported it.
	Phone string
}

// UnreadableMapping is a record cleanup found in the mappings bucket but could
// not decode. The record is kept on disk for an operator to inspect; this value
// is the report of why it was left there.
type UnreadableMapping struct {
	// Key is the storage key, "chatID:telegramMessageID", of the record.
	Key string
	// Err is the decode failure, wrapped by ErrInvalidMapping.
	Err error
}

// CleanupResult is what a cleanup pass removed and what it refused to touch.
type CleanupResult struct {
	// Removed is how many expired mappings were deleted.
	Removed int
	// Skipped lists the unreadable records the pass kept on disk.
	Skipped []UnreadableMapping
}

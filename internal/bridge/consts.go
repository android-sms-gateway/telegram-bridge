package bridge

// forwardMessageFormat is the rendering of a forwarded SMS, matching the
// example-telegram-forwarder-fn contract (HTML parse mode).
const forwardMessageFormat = "New message from <b>%s</b>:\n<code>%s</code>"

// unknownMessageID is reported instead of an empty id, which the SMSGate server
// leaves out when it answers a conflict for an already queued message.
const unknownMessageID = "unknown"

const (
	// OutcomeEmptyText means the reply carried no text (a photo, for example):
	// there is nothing to send.
	OutcomeEmptyText OutcomeKind = iota

	// OutcomeNotForwarded means the replied-to message does not resolve to a
	// sender. The bridge never forwarded it, or its mapping has expired.
	OutcomeNotForwarded

	// OutcomeLookupFailed means the mapping lookup failed for any other reason,
	// for example an unreadable database.
	OutcomeLookupFailed

	// OutcomeSendFailed means the SMSGate server refused or did not answer the
	// send.
	OutcomeSendFailed

	// OutcomeSent means the message was queued for the resolved sender.
	OutcomeSent
)

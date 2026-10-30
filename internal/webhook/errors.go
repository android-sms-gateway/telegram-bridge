// Package webhook receives SMSGate webhook events and forwards them to
// Telegram.
//
// The ingest contract mirrors the SMSGate app webhook envelope: a POST with
// JSON {payload: {...}} authenticated by the SMSGate payload signature scheme
// (HMAC-SHA256 over raw body + X-Timestamp, hex-encoded in the X-Signature
// header; see the webhooks guide at docs.sms-gate.app).
package webhook

import "errors"

var (
	// ErrInvalidSignature is returned when the request has no signature, no
	// timestamp, or the signature does not match the configured signing key.
	ErrInvalidSignature = errors.New("webhook: missing or invalid signature")
)

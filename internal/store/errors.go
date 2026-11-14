// Package store is the durable state of the SMSGate -> Telegram bridge.
//
// It keeps the mapping that resolves a Telegram reply back to the SMS sender
// (chat id plus Telegram message id -> phone number) in the embedded bbolt
// database that pkg/boltfx opens from the raw storage.url setting.
//
// Schema:
//
//   - "mappings": key "<chatID>:<telegramMessageID>", value a JSON encoded
//     Mapping {phone, created_at, expires_at}
//
// A mapping stays resolvable until its expires_at: Resolve enforces the expiry
// on read and the background cleanup drops the expired records.
package store

import "errors"

var (
	// ErrMappingNotFound is returned when no mapping is stored for the requested
	// chat and Telegram message pair, or when the stored mapping has expired.
	ErrMappingNotFound = errors.New("mapping not found")

	// ErrInvalidMapping is returned when a stored record cannot be decoded into
	// a Mapping. It means the database holds data this version cannot read.
	ErrInvalidMapping = errors.New("invalid mapping")

	// ErrBucketMissing is returned when the mappings bucket is absent. The
	// repository creates it at construction, so this means the database was
	// modified behind its back.
	ErrBucketMissing = errors.New("bucket is missing")
)

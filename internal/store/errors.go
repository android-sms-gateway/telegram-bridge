// Package store is the durable state of the SMSGate -> Telegram bridge.
//
// It keeps the mapping that resolves a Telegram reply back to the SMS sender
// (chat id plus Telegram message id -> phone number) and the long-polling
// offset, both in the embedded bbolt database that pkg/boltfx opens from the raw
// storage.url setting.
//
// Schema:
//
//   - "mappings": key "<chatID>:<telegramMessageID>", value a JSON encoded
//     Mapping {phone, created_at, expires_at}
//   - "offsets": the single key "offset", value the getUpdates offset as a
//     decimal number
//
// A mapping stays resolvable until its expires_at: Resolve enforces the expiry
// on read and the background cleanup drops the expired records. The cleanup
// only ever walks the mappings bucket, never offsets.
package store

import "errors"

var (
	// ErrMappingNotFound is returned when no mapping is stored for the requested
	// chat and Telegram message pair, or when the stored mapping has expired.
	ErrMappingNotFound = errors.New("store: mapping not found")

	// ErrInvalidMapping is returned when a stored record cannot be decoded into
	// a Mapping. It means the database holds data this version cannot read.
	ErrInvalidMapping = errors.New("store: invalid mapping")

	// ErrInvalidOffset is returned when the stored offset is not a decimal
	// number.
	ErrInvalidOffset = errors.New("store: invalid offset")

	// ErrBucketMissing is returned when one of the two buckets is absent. The
	// repository creates both of them at construction, so this means the
	// database was modified behind its back.
	ErrBucketMissing = errors.New("store: bucket is missing")
)

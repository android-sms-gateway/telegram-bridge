package store

import (
	"encoding/json"
	"fmt"
	"time"
)

// mappingModel is the durable record that resolves a Telegram reply back to the SMS
// sender it was forwarded from.
type mappingModel struct {
	// Phone is the SMS sender number the message was forwarded from, exactly as
	// the SMSGate webhook reported it.
	Phone string `json:"phone"`

	// CreatedAt is when the mapping was written.
	CreatedAt time.Time `json:"created_at"`

	// ExpiresAt is when the mapping stops being resolvable. It is stored with
	// the record instead of being derived from the TTL, so the TTL of a write
	// never changes retroactively.
	ExpiresAt time.Time `json:"expires_at"`
}

// IsExpired reports whether a mapping is no longer resolvable at now. The
// comparison is inclusive: a mapping whose expires_at equals now is expired, so
// the same rule holds on read and in the cleanup.
func (m *mappingModel) IsExpired(now time.Time) bool {
	return !now.Before(m.ExpiresAt)
}

// newMappingModel is the single creation point of a record to be written: it
// stamps the write time and applies the TTL, so every record in the bucket
// carries the same lifetime policy and no caller can write its own timestamps.
func newMappingModel(m Mapping, now time.Time, ttl time.Duration) mappingModel {
	return mappingModel{
		Phone:     m.Phone,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
}

// MarshalStorage encodes the record into its durable wire form. The format is
// the model's own concern: the repository stores and reads bytes only.
func (m *mappingModel) MarshalStorage() ([]byte, error) {
	payload, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("marshal mapping model: %w", err)
	}

	return payload, nil
}

// UnmarshalStorage decodes a durable wire record into the model. It wraps the
// encoding error but does not classify it: an undecodable record means
// different things to different operations, and the caller decides.
func (m *mappingModel) UnmarshalStorage(raw []byte) error {
	if err := json.Unmarshal(raw, m); err != nil {
		return fmt.Errorf("unmarshal mapping model: %w", err)
	}

	return nil
}

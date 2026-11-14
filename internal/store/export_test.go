package store

import (
	"context"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// PutWithTimesForTest writes a mapping record with explicit created_at and
// expires_at, the lifetimes the public API deliberately no longer accepts:
// timestamps are storage policy, stamped by newMappingModel. The record is
// built through newMappingModel, so a seeded row has exactly the shape Put
// writes.
func PutWithTimesForTest(
	ctx context.Context,
	r *Repository,
	ref MessageRef,
	phone string,
	createdAt time.Time,
	expiresAt time.Time,
) error {
	if cerr := checkContext(ctx); cerr != nil {
		return fmt.Errorf("put mapping for test: %w", cerr)
	}

	model := newMappingModel(
		Mapping{MessageRef: ref, Phone: phone},
		createdAt,
		expiresAt.Sub(createdAt),
	)

	payload, merr := model.MarshalStorage()
	if merr != nil {
		return fmt.Errorf("put mapping for test: %w", merr)
	}

	if uerr := r.db.Update(func(tx *bolt.Tx) error {
		bucket := mappings(tx)
		if bucket == nil {
			return fmt.Errorf("%w: %s", ErrBucketMissing, mappingsBucket)
		}

		return bucket.Put([]byte(ref.key()), payload)
	}); uerr != nil {
		return fmt.Errorf("put mapping for test: %w", uerr)
	}

	return nil
}

// MappingModelForTest reads the stored phone and timestamps of a record — the
// non-domain invariants Resolve no longer returns — straight from the bucket.
func MappingModelForTest(db *bolt.DB, ref MessageRef) (string, time.Time, time.Time, error) {
	var (
		phone     string
		createdAt time.Time
		expiresAt time.Time
	)

	if err := db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(mappingsBucket))
		if bucket == nil {
			return fmt.Errorf("%w: %s", ErrBucketMissing, mappingsBucket)
		}

		raw := bucket.Get([]byte(ref.key()))
		if len(raw) == 0 {
			return fmt.Errorf("%w: %s", ErrMappingNotFound, ref.key())
		}

		var model mappingModel
		if uerr := model.UnmarshalStorage(raw); uerr != nil {
			return fmt.Errorf("%w: %w", ErrInvalidMapping, uerr)
		}

		phone, createdAt, expiresAt = model.Phone, model.CreatedAt, model.ExpiresAt

		return nil
	}); err != nil {
		return "", time.Time{}, time.Time{}, fmt.Errorf("read mapping model: %w", err)
	}

	return phone, createdAt, expiresAt, nil
}

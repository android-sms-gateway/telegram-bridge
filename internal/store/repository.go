package store

import (
	"bytes"
	"context"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

const (
	// mappingsBucket holds one JSON encoded mappingModel per forwarded message,
	// keyed by chat id and Telegram message id.
	mappingsBucket = "mappings"
)

// Repository reads and writes the durable bridge state in the bbolt database
// opened by pkg/boltfx.
//
// bbolt serializes write transactions and runs read transactions concurrently.
// Every operation opens its own short-lived transaction: no transaction is ever
// held across an external call, so a slow reader cannot block the writer and the
// repository is safe for concurrent use without extra locking.
type Repository struct {
	db  *bolt.DB
	ttl time.Duration
}

// NewRepository creates the buckets of a Repository and returns it.
//
// cfg carries the mapping TTL; a zero or negative TTL means DefaultTTL. The
// caller keeps ownership of db: pkg/boltfx closes it when the application stops.
func NewRepository(cfg Config, db *bolt.DB) (*Repository, error) {
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}

	repo := &Repository{
		db:  db,
		ttl: ttl,
	}

	if err := db.Update(createBuckets); err != nil {
		return nil, fmt.Errorf("create buckets: %w", err)
	}

	return repo, nil
}

// Put stores the mapping that resolves a reply to the referenced message back
// to the SMS sender.
//
// The domain carries only identity and phone: the record's created_at and
// expires_at are stamped here through newMappingModel from the configured TTL,
// so no caller decides the lifetime of a write. Writing the same ref twice
// replaces the record.
func (r *Repository) Put(ctx context.Context, mapping Mapping) error {
	if cerr := checkContext(ctx); cerr != nil {
		return fmt.Errorf("put mapping: %w", cerr)
	}

	model := newMappingModel(mapping, time.Now(), r.ttl)

	payload, merr := model.MarshalStorage()
	if merr != nil {
		return fmt.Errorf("put mapping: %w", merr)
	}

	key := mapping.key()

	err := r.db.Update(func(tx *bolt.Tx) error {
		bucket := mappings(tx)
		if bucket == nil {
			return fmt.Errorf("%w: %s", ErrBucketMissing, mappingsBucket)
		}

		if perr := bucket.Put([]byte(key), payload); perr != nil {
			return fmt.Errorf("put mapping: %w", perr)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("put mapping: %w", err)
	}

	return nil
}

// Resolve returns the mapping of the message ref refers to.
//
// A missing record and an expired one are reported the same way, with
// ErrMappingNotFound: from the caller both mean "this reply cannot be resolved
// to a sender". The record is left on disk either way, the cleanup loop removes
// it later.
func (r *Repository) Resolve(ctx context.Context, ref MessageRef) (Mapping, error) {
	if cerr := checkContext(ctx); cerr != nil {
		return Mapping{}, fmt.Errorf("resolve mapping: %w", cerr)
	}

	var model mappingModel

	key := ref.key()

	err := r.db.View(func(tx *bolt.Tx) error {
		bucket := mappings(tx)
		if bucket == nil {
			return fmt.Errorf("%w: %s", ErrBucketMissing, mappingsBucket)
		}

		raw := bucket.Get([]byte(key))
		if len(raw) == 0 {
			return ErrMappingNotFound
		}

		if uerr := model.UnmarshalStorage(raw); uerr != nil {
			return fmt.Errorf("%w: %w", ErrInvalidMapping, uerr)
		}

		if model.IsExpired(time.Now()) {
			return ErrMappingNotFound
		}

		return nil
	})
	if err != nil {
		return Mapping{}, fmt.Errorf("resolve mapping: %w", err)
	}

	return Mapping{MessageRef: ref, Phone: model.Phone}, nil
}

// CleanupExpired removes every mapping whose expires_at is not in the future
// and returns what it removed and what it kept.
//
// The expired keys are collected first and deleted afterwards inside the same
// transaction: mutating a bucket while a cursor walks it can make that cursor
// return unexpected keys. Deciding and deleting in one transaction also keeps
// the result consistent with a concurrent Put, which either happens before the
// scan and is deleted, or after it and survives.
func (r *Repository) CleanupExpired(ctx context.Context) (CleanupResult, error) {
	if cerr := checkContext(ctx); cerr != nil {
		return CleanupResult{}, fmt.Errorf("cleanup expired: %w", cerr)
	}

	var result CleanupResult

	err := r.db.Update(func(tx *bolt.Tx) error {
		bucket := mappings(tx)
		if bucket == nil {
			return fmt.Errorf("%w: %s", ErrBucketMissing, mappingsBucket)
		}

		keys, skipped := expiredKeys(bucket, time.Now())
		result.Skipped = skipped

		for _, key := range keys {
			if derr := bucket.Delete(key); derr != nil {
				return fmt.Errorf("delete mapping: %w", derr)
			}

			result.Removed++
		}

		return nil
	})
	if err != nil {
		return CleanupResult{}, fmt.Errorf("cleanup expired: %w", err)
	}

	return result, nil
}

// createBuckets creates both buckets of the schema if they are absent.
func createBuckets(tx *bolt.Tx) error {
	for _, name := range []string{mappingsBucket} {
		if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
			return fmt.Errorf("create bucket %s: %w", name, err)
		}
	}

	return nil
}

// mappings returns the mappings bucket of a transaction, or nil when the bucket
// does not exist.
func mappings(tx *bolt.Tx) *bolt.Bucket {
	return tx.Bucket([]byte(mappingsBucket))
}

// expiredKeys collects the keys of every mapping that is expired at now, with
// the unreadable records the pass skips.
//
// A record that cannot be decoded is kept and reported: Resolve fails it with
// ErrInvalidMapping rather than treating it as missing, and deleting data on a
// guess is worse than leaving it for an operator to inspect.
func expiredKeys(bucket *bolt.Bucket, now time.Time) ([][]byte, []UnreadableMapping) {
	keys := [][]byte{}

	var skipped []UnreadableMapping

	cursor := bucket.Cursor()
	for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
		var model mappingModel
		if err := model.UnmarshalStorage(raw); err != nil {
			skipped = append(skipped, UnreadableMapping{
				Key: string(key),
				Err: fmt.Errorf("%w: %w", ErrInvalidMapping, err),
			})

			continue
		}

		if !model.IsExpired(now) {
			continue
		}

		// The cursor memory is only valid while the cursor stays where it is, so
		// the key is copied before the deletes that follow the walk.
		keys = append(keys, bytes.Clone(key))
	}

	return keys, skipped
}

// checkContext reports the context error before a transaction is opened. bbolt
// transactions are synchronous and take no context, so a cancelled context can
// only be honored at the boundary of an operation.
func checkContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("context: %w", err)
	}

	return nil
}

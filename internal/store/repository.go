package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
	"go.uber.org/zap"
)

const (
	// mappingsBucket holds one JSON encoded Mapping per forwarded message,
	// keyed by chat id and Telegram message id.
	mappingsBucket = "mappings"

	// offsetsBucket holds the long-polling offset of a single well-known key.
	// Mappings never expire here, so the cleanup loop leaves it alone.
	offsetsBucket = "offsets"

	// offsetKey is the only key of offsetsBucket.
	offsetKey = "offset"

	// mappingKeySeparator joins the chat id and the Telegram message id. A
	// Telegram chat id is a signed integer and never contains it, so the joined
	// key is unambiguous.
	mappingKeySeparator = ":"
)

// Mapping is the durable record that resolves a Telegram reply back to the SMS
// sender it was forwarded from.
type Mapping struct {
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

	logger *zap.Logger
}

// NewRepository creates the buckets of a Repository and returns it.
//
// cfg carries the mapping TTL; a zero or negative TTL means DefaultTTL. The
// caller keeps ownership of db: pkg/boltfx closes it when the application stops.
func NewRepository(cfg Config, db *bolt.DB, logger *zap.Logger) (*Repository, error) {
	ttl := cfg.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}

	repo := &Repository{
		db:  db,
		ttl: ttl,

		logger: logger,
	}

	if err := db.Update(createBuckets); err != nil {
		return nil, fmt.Errorf("store: create buckets: %w", err)
	}

	logger.Debug("store buckets ready")

	return repo, nil
}

// Put stores the mapping that resolves a reply to tgMessageID in chatID back to
// the SMS sender.
//
// A zero Mapping.CreatedAt is stamped with the current time and a zero
// Mapping.ExpiresAt with CreatedAt plus the configured TTL, so a caller that
// only knows the phone number still gets a bounded lifetime. Writing the same
// pair twice replaces the record.
func (r *Repository) Put(ctx context.Context, chatID int64, tgMessageID int, mapping Mapping) error {
	if cerr := checkContext(ctx); cerr != nil {
		return fmt.Errorf("store: put mapping: %w", cerr)
	}

	if mapping.CreatedAt.IsZero() {
		mapping.CreatedAt = time.Now()
	}

	if mapping.ExpiresAt.IsZero() {
		mapping.ExpiresAt = mapping.CreatedAt.Add(r.ttl)
	}

	payload, merr := json.Marshal(mapping)
	if merr != nil {
		return fmt.Errorf("store: put mapping: marshal: %w", merr)
	}

	key := mappingKey(chatID, tgMessageID)

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
		return fmt.Errorf("store: put mapping: %w", err)
	}

	r.logger.Debug("mapping stored",
		zap.String("key", key),
		zap.String("phone", mapping.Phone),
		zap.Time("expires_at", mapping.ExpiresAt),
	)

	return nil
}

// Resolve returns the mapping of a forwarded message.
//
// A missing record and an expired one are reported the same way, with
// ErrMappingNotFound: from the caller both mean "this reply cannot be resolved
// to a sender". The record is left on disk either way, the cleanup loop removes
// it later.
func (r *Repository) Resolve(ctx context.Context, chatID int64, tgMessageID int) (Mapping, error) {
	if cerr := checkContext(ctx); cerr != nil {
		return Mapping{}, fmt.Errorf("store: resolve mapping: %w", cerr)
	}

	var mapping Mapping

	key := mappingKey(chatID, tgMessageID)

	err := r.db.View(func(tx *bolt.Tx) error {
		bucket := mappings(tx)
		if bucket == nil {
			return fmt.Errorf("%w: %s", ErrBucketMissing, mappingsBucket)
		}

		raw := bucket.Get([]byte(key))
		if len(raw) == 0 {
			return ErrMappingNotFound
		}

		if uerr := json.Unmarshal(raw, &mapping); uerr != nil {
			return fmt.Errorf("%w: %w", ErrInvalidMapping, uerr)
		}

		if isExpired(mapping, time.Now()) {
			return ErrMappingNotFound
		}

		return nil
	})
	if err != nil {
		return Mapping{}, fmt.Errorf("store: resolve mapping: %w", err)
	}

	return mapping, nil
}

// CleanupExpired removes every mapping whose expires_at is not in the future
// and returns how many were removed. The offsets bucket is never touched.
//
// The expired keys are collected first and deleted afterwards inside the same
// transaction: mutating a bucket while a cursor walks it can make that cursor
// return unexpected keys. Deciding and deleting in one transaction also keeps
// the result consistent with a concurrent Put, which either happens before the
// scan and is deleted, or after it and survives.
func (r *Repository) CleanupExpired(ctx context.Context) (int, error) {
	if cerr := checkContext(ctx); cerr != nil {
		return 0, fmt.Errorf("store: cleanup expired: %w", cerr)
	}

	var removed int

	err := r.db.Update(func(tx *bolt.Tx) error {
		bucket := mappings(tx)
		if bucket == nil {
			return fmt.Errorf("%w: %s", ErrBucketMissing, mappingsBucket)
		}

		for _, key := range expiredKeys(bucket, time.Now(), r.logger) {
			if derr := bucket.Delete(key); derr != nil {
				return fmt.Errorf("delete mapping: %w", derr)
			}

			removed++
		}

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("store: cleanup expired: %w", err)
	}

	return removed, nil
}

// SetOffset persists the long-polling offset, so a restart does not replay the
// updates that were already handled.
func (r *Repository) SetOffset(ctx context.Context, offset int64) error {
	if cerr := checkContext(ctx); cerr != nil {
		return fmt.Errorf("store: set offset: %w", cerr)
	}

	value := strconv.FormatInt(offset, 10)

	err := r.db.Update(func(tx *bolt.Tx) error {
		bucket := offsets(tx)
		if bucket == nil {
			return fmt.Errorf("%w: %s", ErrBucketMissing, offsetsBucket)
		}

		if perr := bucket.Put([]byte(offsetKey), []byte(value)); perr != nil {
			return fmt.Errorf("put offset: %w", perr)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("store: set offset: %w", err)
	}

	r.logger.Debug("offset stored", zap.Int64("offset", offset))

	return nil
}

// GetOffset returns the persisted long-polling offset. An offset that was never
// stored is reported as zero without an error: a fresh installation has nothing
// to skip.
func (r *Repository) GetOffset(ctx context.Context) (int64, error) {
	if cerr := checkContext(ctx); cerr != nil {
		return 0, fmt.Errorf("store: get offset: %w", cerr)
	}

	var offset int64

	err := r.db.View(func(tx *bolt.Tx) error {
		bucket := offsets(tx)
		if bucket == nil {
			return fmt.Errorf("%w: %s", ErrBucketMissing, offsetsBucket)
		}

		raw := bucket.Get([]byte(offsetKey))
		if len(raw) == 0 {
			return nil
		}

		parsed, perr := strconv.ParseInt(string(raw), 10, 64)
		if perr != nil {
			return fmt.Errorf("%w: %s: %w", ErrInvalidOffset, raw, perr)
		}

		offset = parsed

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("store: get offset: %w", err)
	}

	return offset, nil
}

// createBuckets creates both buckets of the schema if they are absent.
func createBuckets(tx *bolt.Tx) error {
	for _, name := range []string{mappingsBucket, offsetsBucket} {
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

// offsets returns the offsets bucket of a transaction, or nil when the bucket
// does not exist.
func offsets(tx *bolt.Tx) *bolt.Bucket {
	return tx.Bucket([]byte(offsetsBucket))
}

// mappingKey builds the mappings bucket key of a forwarded message.
func mappingKey(chatID int64, tgMessageID int) string {
	return fmt.Sprintf("%d%s%d", chatID, mappingKeySeparator, tgMessageID)
}

// isExpired reports whether a mapping is no longer resolvable at now. The
// comparison is inclusive: a mapping whose expires_at equals now is expired, so
// the same rule holds on read and in the cleanup.
func isExpired(mapping Mapping, now time.Time) bool {
	return !now.Before(mapping.ExpiresAt)
}

// expiredKeys collects the keys of every mapping that is expired at now.
//
// A record that cannot be decoded is kept and reported: it is skipped by
// Resolve already, and deleting data on a guess is worse than leaving it for an
// operator to inspect.
func expiredKeys(bucket *bolt.Bucket, now time.Time, logger *zap.Logger) [][]byte {
	keys := [][]byte{}

	cursor := bucket.Cursor()
	for key, raw := cursor.First(); key != nil; key, raw = cursor.Next() {
		var mapping Mapping
		if err := json.Unmarshal(raw, &mapping); err != nil {
			logger.Warn("skipping unreadable mapping", zap.String("key", string(key)), zap.Error(err))

			continue
		}

		if !isExpired(mapping, now) {
			continue
		}

		// The cursor memory is only valid while the cursor stays where it is, so
		// the key is copied before the deletes that follow the walk.
		keys = append(keys, bytes.Clone(key))
	}

	return keys
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

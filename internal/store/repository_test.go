package store_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/android-sms-gateway/telegram-bridge/internal/store"
	"github.com/go-core-fx/boltfx"
	bolt "go.etcd.io/bbolt"
	"go.uber.org/zap"
)

// Names of the documented schema. The tests assert them against the real
// buckets so a rename of the unexported constants cannot pass unnoticed.
const (
	mappingsBucketName = "mappings"
)

const (
	testChatID  int64 = 123456789
	testGroupID int64 = -1001234567890
	testChannel int64 = -1009876543210

	testMsgID = 42
	testPhone = "6505551212"

	// pollInterval and pollTimeout bound the wait for the background cleanup,
	// which runs in its own goroutine.
	pollInterval = 10 * time.Millisecond
	pollTimeout  = 5 * time.Second
)

// openTestDB opens a bolt database at path and closes it when the test ends.
func openTestDB(t *testing.T, path string) *bolt.DB {
	t.Helper()

	db, err := boltfx.New(boltfx.Config{URL: "bolt://" + path}, zap.NewNop())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close database: %v", cerr)
		}
	})

	return db
}

// newTestRepository opens a database in a temporary directory with the default
// TTL, and returns the repository and the raw handle to inspect its buckets.
func newTestRepository(t *testing.T) (*store.Repository, *bolt.DB) {
	t.Helper()

	db := openTestDB(t, filepath.Join(t.TempDir(), "store.db"))

	repo, err := store.NewRepository(store.Config{TTL: store.DefaultTTL}, db)
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}

	return repo, db
}

// countBucket returns how many records a bucket holds.
func countBucket(t *testing.T, db *bolt.DB, name string) int {
	t.Helper()

	count := 0

	err := db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(name))
		if bucket == nil {
			t.Fatalf("bucket %q is missing", name)
		}

		return bucket.ForEach(func(_, _ []byte) error {
			count++

			return nil
		})
	})
	if err != nil {
		t.Fatalf("count bucket %q: %v", name, err)
	}

	return count
}

// ref is the identity of a forwarded message in the store API.
func ref(chatID int64, msgID int) store.MessageRef {
	return store.MessageRef{ChatID: chatID, MessageID: msgID}
}

// zeroMapping is the Mapping a failed read returns.
func zeroMapping() store.Mapping {
	return store.Mapping{}
}

// putAt writes a mapping with the given lifetime into a fresh repository.
//
// A positive lifetime is written through Put, which stamps it from the
// repository's configured TTL (every caller passes DefaultTTL against a
// DefaultTTL repository). An already-expired record cannot be written through
// the public API — timestamps are storage policy — so the negative case seeds
// the bucket through the package's test helper.
func putAt(t *testing.T, repo *store.Repository, chatID int64, msgID int, phone string, expiresIn time.Duration) {
	t.Helper()

	var err error
	if expiresIn > 0 {
		err = repo.Put(context.Background(), store.Mapping{MessageRef: ref(chatID, msgID), Phone: phone})
	} else {
		now := time.Now()
		err = store.PutWithTimesForTest(
			context.Background(), repo, ref(chatID, msgID), phone, now, now.Add(expiresIn),
		)
	}
	if err != nil {
		t.Fatalf("put mapping: %v", err)
	}
}

// waitForBucketCount waits until a bucket holds want records. The cleanup loop
// runs in its own goroutine, so the assertion cannot be a plain read.
func waitForBucketCount(t *testing.T, db *bolt.DB, name string, want int) {
	t.Helper()

	deadline := time.Now().Add(pollTimeout)
	for time.Now().Before(deadline) {
		if countBucket(t, db, name) == want {
			return
		}

		time.Sleep(pollInterval)
	}

	t.Fatalf("bucket %q holds %d records, want %d", name, countBucket(t, db, name), want)
}

func TestPutResolveRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		chatID      int64
		tgMessageID int
		phone       string
	}{
		{name: "private chat", chatID: testChatID, tgMessageID: testMsgID, phone: testPhone},
		{name: "supergroup chat", chatID: testGroupID, tgMessageID: 7, phone: "+15551234567"},
		{name: "channel chat", chatID: testChannel, tgMessageID: 1, phone: "380501234567"},
		{name: "lowest message id", chatID: testChatID, tgMessageID: 0, phone: testPhone},
		{name: "large message id", chatID: testChatID, tgMessageID: 1 << 30, phone: testPhone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			repo, db := newTestRepository(t)

			putAt(t, repo, tt.chatID, tt.tgMessageID, tt.phone, store.DefaultTTL)

			got, err := repo.Resolve(context.Background(), ref(tt.chatID, tt.tgMessageID))
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}

			if got.MessageRef != ref(tt.chatID, tt.tgMessageID) {
				t.Fatalf("Resolve().MessageRef = %+v, want %+v", got.MessageRef, ref(tt.chatID, tt.tgMessageID))
			}

			if got.Phone != tt.phone {
				t.Fatalf("Resolve().Phone = %q, want %q", got.Phone, tt.phone)
			}

			// created_at/expires_at are storage policy, not part of the domain
			// Resolve returns, so the stamping is asserted on the stored record.
			_, createdAt, expiresAt, merr := store.MappingModelForTest(db, ref(tt.chatID, tt.tgMessageID))
			if merr != nil {
				t.Fatalf("read stored model: %v", merr)
			}

			if createdAt.IsZero() {
				t.Error("stored created_at is zero, want the write timestamp")
			}

			if !expiresAt.After(createdAt) {
				t.Errorf("stored expires_at = %v, want it after created_at %v", expiresAt, createdAt)
			}

			if expiresAt.Sub(createdAt) != store.DefaultTTL {
				t.Errorf(
					"stored lifetime = %v, want %v",
					expiresAt.Sub(createdAt),
					store.DefaultTTL,
				)
			}

			// The key is "<chatID>:<telegramMessageID>": the schema, not an
			// internal detail, is what makes a record addressable.
			key := fmt.Sprintf("%d:%d", tt.chatID, tt.tgMessageID)

			err = db.View(func(tx *bolt.Tx) error {
				raw := tx.Bucket([]byte(mappingsBucketName)).Get([]byte(key))
				if len(raw) == 0 {
					t.Errorf("mappings bucket has no record for key %q", key)
				}

				return nil
			})
			if err != nil {
				t.Fatalf("view mappings: %v", err)
			}
		})
	}
}

func TestPutReplacesExistingMapping(t *testing.T) {
	t.Parallel()

	repo, db := newTestRepository(t)

	putAt(t, repo, testChatID, testMsgID, testPhone, store.DefaultTTL)
	putAt(t, repo, testChatID, testMsgID, "+15550000000", store.DefaultTTL)

	got, err := repo.Resolve(context.Background(), ref(testChatID, testMsgID))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if got.Phone != "+15550000000" {
		t.Fatalf("Resolve().Phone = %q, want the last written number", got.Phone)
	}

	if count := countBucket(t, db, mappingsBucketName); count != 1 {
		t.Fatalf("mappings bucket holds %d records, want 1", count)
	}
}

func TestPutAppliesConfiguredTTL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  store.Config
		want time.Duration
	}{
		{name: "configured ttl", cfg: store.Config{TTL: 2 * time.Hour}, want: 2 * time.Hour},
		{name: "zero ttl falls back to the default", cfg: store.Config{TTL: 0}, want: store.DefaultTTL},
		{name: "negative ttl falls back to the default", cfg: store.Config{TTL: -time.Minute}, want: store.DefaultTTL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "ttl.db")
			db := openTestDB(t, path)

			repo, err := store.NewRepository(tt.cfg, db)
			if err != nil {
				t.Fatalf("create repository: %v", err)
			}

			// The domain carries no timestamps: Put has to stamp both through
			// newMappingModel, or the mapping would be born already expired.
			err = repo.Put(context.Background(), store.Mapping{
				MessageRef: ref(testChatID, testMsgID),
				Phone:      testPhone,
			})
			if err != nil {
				t.Fatalf("Put() error = %v", err)
			}

			_, createdAt, expiresAt, merr := store.MappingModelForTest(db, ref(testChatID, testMsgID))
			if merr != nil {
				t.Fatalf("read stored model: %v", merr)
			}

			if createdAt.IsZero() {
				t.Error("Put() left created_at zero, want it stamped")
			}

			if lifetime := expiresAt.Sub(createdAt); lifetime != tt.want {
				t.Errorf("Put() lifetime = %v, want %v", lifetime, tt.want)
			}
		})
	}
}

func TestResolveUnknownMapping(t *testing.T) {
	t.Parallel()

	repo, _ := newTestRepository(t)

	putAt(t, repo, testChatID, testMsgID, testPhone, store.DefaultTTL)

	tests := []struct {
		name        string
		chatID      int64
		tgMessageID int
	}{
		{name: "empty store", chatID: 1, tgMessageID: 1},
		{name: "unknown chat", chatID: testGroupID, tgMessageID: testMsgID},
		{name: "unknown message", chatID: testChatID, tgMessageID: testMsgID + 1},
		{name: "message id zero", chatID: testChatID, tgMessageID: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := repo.Resolve(context.Background(), ref(tt.chatID, tt.tgMessageID))
			if !errors.Is(err, store.ErrMappingNotFound) {
				t.Fatalf("Resolve() error = %v, want %v", err, store.ErrMappingNotFound)
			}

			if got != zeroMapping() {
				t.Fatalf("Resolve() = %+v, want the zero Mapping", got)
			}
		})
	}
}

func TestResolveExpiredMapping(t *testing.T) {
	t.Parallel()

	repo, db := newTestRepository(t)

	putAt(t, repo, testChatID, testMsgID, testPhone, -time.Second)
	putAt(t, repo, testChatID, testMsgID+1, testPhone, store.DefaultTTL)

	tests := []struct {
		name        string
		tgMessageID int
	}{
		{name: "expired a second ago", tgMessageID: testMsgID},
		{name: "still valid", tgMessageID: testMsgID + 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := repo.Resolve(context.Background(), ref(testChatID, tt.tgMessageID))
			if tt.tgMessageID == testMsgID {
				if !errors.Is(err, store.ErrMappingNotFound) {
					t.Fatalf("Resolve() error = %v, want %v", err, store.ErrMappingNotFound)
				}

				return
			}

			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
		})
	}

	// Expiry is enforced on read: the expired record is still on disk, only the
	// periodic cleanup removes it.
	if count := countBucket(t, db, mappingsBucketName); count != 2 {
		t.Fatalf("mappings bucket holds %d records, want 2 (read must not delete)", count)
	}
}

func TestCleanupExpired(t *testing.T) {
	t.Parallel()

	repo, db := newTestRepository(t)

	putAt(t, repo, testChatID, 1, testPhone, -2*time.Hour)
	putAt(t, repo, testChatID, 2, "+15550000001", -time.Millisecond)
	putAt(t, repo, testGroupID, 3, "+15550000002", store.DefaultTTL)
	putAt(t, repo, testGroupID, 4, "+15550000003", store.DefaultTTL)

	result, err := repo.CleanupExpired(context.Background())
	if err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}

	if result.Removed != 2 {
		t.Fatalf("CleanupExpired().Removed = %d, want 2", result.Removed)
	}

	if count := countBucket(t, db, mappingsBucketName); count != 2 {
		t.Fatalf("mappings bucket holds %d records, want 2", count)
	}

	if _, err = repo.Resolve(context.Background(), ref(testChatID, 1)); !errors.Is(err, store.ErrMappingNotFound) {
		t.Fatalf("Resolve(expired) error = %v, want %v", err, store.ErrMappingNotFound)
	}

	if _, err = repo.Resolve(context.Background(), ref(testGroupID, 3)); err != nil {
		t.Fatalf("Resolve(live) error = %v", err)
	}

	// A second pass has nothing left to do.
	if result, err = repo.CleanupExpired(context.Background()); err != nil || result.Removed != 0 {
		t.Fatalf("CleanupExpired() = (%d, %v), want (0, nil)", result.Removed, err)
	}
}

func TestCleanupExpiredOnEmptyStore(t *testing.T) {
	t.Parallel()

	repo, _ := newTestRepository(t)

	result, err := repo.CleanupExpired(context.Background())
	if err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}

	if result.Removed != 0 {
		t.Fatalf("CleanupExpired().Removed = %d, want 0", result.Removed)
	}
}

func TestResolveUnreadableRecord(t *testing.T) {
	t.Parallel()

	repo, db := newTestRepository(t)

	key := fmt.Sprintf("%d:%d", testChatID, testMsgID)

	err := db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte(mappingsBucketName)).Put([]byte(key), []byte("{not json"))
	})
	if err != nil {
		t.Fatalf("seed unreadable mapping: %v", err)
	}

	if _, err = repo.Resolve(
		context.Background(),
		ref(testChatID, testMsgID),
	); !errors.Is(
		err,
		store.ErrInvalidMapping,
	) {
		t.Fatalf("Resolve() error = %v, want %v", err, store.ErrInvalidMapping)
	}

	// The cleanup only trusts records it can decode, so it leaves this one for
	// an operator instead of guessing what it was — and reports why.
	result, err := repo.CleanupExpired(context.Background())
	if err != nil {
		t.Fatalf("CleanupExpired() error = %v", err)
	}

	if result.Removed != 0 {
		t.Fatalf("CleanupExpired().Removed = %d, want 0", result.Removed)
	}

	if len(result.Skipped) != 1 {
		t.Fatalf("CleanupExpired().Skipped holds %d records, want 1", len(result.Skipped))
	}

	if result.Skipped[0].Key != key {
		t.Errorf("CleanupExpired().Skipped[0].Key = %q, want %q", result.Skipped[0].Key, key)
	}

	if !errors.Is(result.Skipped[0].Err, store.ErrInvalidMapping) {
		t.Errorf(
			"CleanupExpired().Skipped[0].Err = %v, want it to wrap %v",
			result.Skipped[0].Err,
			store.ErrInvalidMapping,
		)
	}

	if count := countBucket(t, db, mappingsBucketName); count != 1 {
		t.Fatalf("mappings bucket holds %d records, want 1", count)
	}
}

func TestPersistenceAcrossReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "persist.db")
	db := openTestDB(t, path)

	repo, err := store.NewRepository(store.Config{TTL: store.DefaultTTL}, db)
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}

	putAt(t, repo, testChatID, testMsgID, testPhone, store.DefaultTTL)

	if err = db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	// Reopen the same file through boltfx, the way a restart does.
	reopenedDB := openTestDB(t, path)

	reopened, err := store.NewRepository(store.Config{TTL: store.DefaultTTL}, reopenedDB)
	if err != nil {
		t.Fatalf("recreate repository: %v", err)
	}

	got, err := reopened.Resolve(context.Background(), ref(testChatID, testMsgID))
	if err != nil {
		t.Fatalf("Resolve() after reopen error = %v", err)
	}

	if got.Phone != testPhone {
		t.Fatalf("Resolve().Phone after reopen = %q, want %q", got.Phone, testPhone)
	}
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()

	repo, _ := newTestRepository(t)

	const (
		writers    = 8
		iterations = 25
	)

	errs := make(chan error, writers+1)
	done := make(chan struct{})

	for w := range writers {
		go func() {
			defer func() { done <- struct{}{} }()

			for i := range iterations {
				chatID := testChatID + int64(w)
				msgID := i

				phone := fmt.Sprintf("+%d", 15550000000+w*100+i)

				if err := repo.Put(context.Background(), store.Mapping{
					MessageRef: ref(chatID, msgID),
					Phone:      phone,
				}); err != nil {
					errs <- err

					return
				}

				if _, err := repo.Resolve(context.Background(), ref(chatID, msgID)); err != nil {
					errs <- err

					return
				}
			}
		}()
	}

	go func() {
		defer func() { done <- struct{}{} }()

		for range iterations {
			if _, err := repo.CleanupExpired(context.Background()); err != nil {
				errs <- err

				return
			}
		}
	}()

	for range writers + 1 {
		<-done
	}

	close(errs)

	for err := range errs {
		t.Fatalf("concurrent store access error: %v", err)
	}
}

func TestCancelledContext(t *testing.T) {
	t.Parallel()

	repo, _ := newTestRepository(t)

	putAt(t, repo, testChatID, testMsgID, testPhone, store.DefaultTTL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	t.Run("Put", func(t *testing.T) {
		t.Parallel()

		err := repo.Put(
			ctx,
			store.Mapping{MessageRef: ref(testChatID, 99), Phone: testPhone},
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Put() error = %v, want it to wrap %v", err, context.Canceled)
		}
	})

	t.Run("Resolve", func(t *testing.T) {
		t.Parallel()

		got, err := repo.Resolve(ctx, ref(testChatID, testMsgID))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Resolve() error = %v, want it to wrap %v", err, context.Canceled)
		}

		if got != zeroMapping() {
			t.Fatalf("Resolve() = %+v, want the zero Mapping", got)
		}
	})

	t.Run("CleanupExpired", func(t *testing.T) {
		t.Parallel()

		result, err := repo.CleanupExpired(ctx)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CleanupExpired() error = %v, want it to wrap %v", err, context.Canceled)
		}

		if result.Removed != 0 {
			t.Fatalf("CleanupExpired().Removed = %d, want 0", result.Removed)
		}
	})
}

func TestClosedDatabase(t *testing.T) {
	t.Parallel()

	repo, db := newTestRepository(t)

	putAt(t, repo, testChatID, testMsgID, testPhone, store.DefaultTTL)

	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "Put",
			call: func() error {
				return repo.Put(
					context.Background(),
					store.Mapping{MessageRef: ref(testChatID, 1), Phone: testPhone},
				)
			},
		},
		{
			name: "Resolve",
			call: func() error {
				_, err := repo.Resolve(context.Background(), ref(testChatID, testMsgID))

				return err
			},
		},
		{
			name: "CleanupExpired",
			call: func() error {
				_, err := repo.CleanupExpired(context.Background())

				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := tt.call(); err == nil {
				t.Fatalf("%s() after close error = nil, want an error", tt.name)
			}
		})
	}
}

func TestNewRepositoryCreatesSchema(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "schema.db")
	db := openTestDB(t, path)

	_, err := store.NewRepository(store.Config{TTL: store.DefaultTTL}, db)
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}

	for _, name := range []string{mappingsBucketName} {
		err = db.View(func(tx *bolt.Tx) error {
			if tx.Bucket([]byte(name)) == nil {
				t.Errorf("bucket %q was not created", name)
			}

			return nil
		})
		if err != nil {
			t.Fatalf("view buckets: %v", err)
		}
	}

	// Creating the repository twice on the same database must be a no-op, not an
	// error: the schema is created with CreateBucketIfNotExists.
	if _, err = store.NewRepository(store.Config{TTL: store.DefaultTTL}, db); err != nil {
		t.Fatalf("recreate repository: %v", err)
	}
}

func TestNewRepositoryOnClosedDatabase(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "closed.db")
	db := openTestDB(t, path)

	if err := db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	repo, err := store.NewRepository(store.Config{TTL: store.DefaultTTL}, db)
	if err == nil {
		t.Fatalf("NewRepository() on a closed database error = nil, want an error")
	}

	if repo != nil {
		t.Fatalf("NewRepository() returned a repository, want nil")
	}
}

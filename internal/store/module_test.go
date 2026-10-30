package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/android-sms-gateway/telegram-bridge/internal/store"
	"github.com/android-sms-gateway/telegram-bridge/pkg/boltfx"
	bolt "go.etcd.io/bbolt"
	bolterrors "go.etcd.io/bbolt/errors"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// seedExpiredMapping writes one already expired mapping into a database at path
// and closes it again, so the caller can open the same file later.
func seedExpiredMapping(t *testing.T, path string) {
	t.Helper()

	db, err := boltfx.New(boltfx.Config{URL: "bolt://" + path}, zap.NewNop())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	repo, err := store.NewRepository(store.Config{TTL: store.DefaultTTL}, db, zap.NewNop())
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}

	putAt(t, repo, testChatID, testMsgID, testPhone, -time.Hour)

	if err = db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}
}

// newModuleApp wires the store module the way internal/commands/serve/serve.go
// does: boltfx opens the file from its config, the store module owns the
// schema and the cleanup loop.
func newModuleApp(t *testing.T, withRun bool, path string) (*fx.App, *store.Repository, *bolt.DB) {
	t.Helper()

	var (
		repo *store.Repository
		db   *bolt.DB
	)

	app := fx.New(
		fx.NopLogger,
		boltfx.Module(),
		fx.Supply(boltfx.Config{URL: "bolt://" + path}),
		fx.Supply(store.Config{TTL: store.DefaultTTL}),
		fx.Supply(zap.NewNop()),
		store.Module(withRun),
		fx.Populate(&repo),
		fx.Populate(&db),
	)

	if err := app.Err(); err != nil {
		t.Fatalf("build app: %v", err)
	}

	return app, repo, db
}

// TestModuleRunsCleanupOnStart asserts the module wires end to end: the
// repository is usable while the application runs, and the cleanup runnable
// drops the mappings that expired while the process was down before its first
// tick.
func TestModuleRunsCleanupOnStart(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "module.db")
	seedExpiredMapping(t, path)

	app, repo, db := newModuleApp(t, true, path)

	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start app: %v", err)
	}

	waitForBucketCount(t, db, mappingsBucketName, 0)

	// The repository stays usable while the cleanup loop runs.
	putAt(t, repo, testChatID, testMsgID+1, testPhone, store.DefaultTTL)

	if _, err := repo.Resolve(context.Background(), testChatID, testMsgID+1); err != nil {
		t.Fatalf("Resolve() while running error = %v", err)
	}

	if err := app.Stop(ctx); err != nil {
		t.Fatalf("stop app: %v", err)
	}

	// The boltfx lifecycle hook owns the handle, so it is closed after stop.
	if err := db.Update(func(_ *bolt.Tx) error { return nil }); !errors.Is(err, bolterrors.ErrDatabaseNotOpen) {
		t.Fatalf("update after stop = %v, want %v", err, bolterrors.ErrDatabaseNotOpen)
	}
}

// TestModuleWithoutRun asserts withRun false skips the cleanup loop: the store
// is still provided, but nothing touches the database in the background.
func TestModuleWithoutRun(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "norun.db")
	seedExpiredMapping(t, path)

	app, repo, db := newModuleApp(t, false, path)

	ctx := context.Background()
	if err := app.Start(ctx); err != nil {
		t.Fatalf("start app: %v", err)
	}

	if count := countBucket(t, db, mappingsBucketName); count != 1 {
		t.Fatalf("mappings bucket holds %d records, want 1 (cleanup must not run)", count)
	}

	if _, err := repo.Resolve(context.Background(), testChatID, testMsgID); err == nil {
		t.Fatal("Resolve() of an expired mapping error = nil, want an error")
	}

	if err := app.Stop(ctx); err != nil {
		t.Fatalf("stop app: %v", err)
	}
}

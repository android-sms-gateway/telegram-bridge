package boltfx_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/android-sms-gateway/telegram-bridge/pkg/boltfx"
	bolt "go.etcd.io/bbolt"
	bolterrors "go.etcd.io/bbolt/errors"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// Fixture bucket and key used by the persistence and lifecycle tests.
const (
	testBucket = "mappings"
	testKey    = "6505551212:42"
)

func TestDBPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want string
	}{
		// The first segment of a relative URL is parsed as the URL host and has
		// to be joined back onto the path.
		{name: "relative single segment", url: "bolt://data/app.db", want: "data/app.db"},
		{name: "relative nested segments", url: "bolt://data/sub/app.db", want: "data/sub/app.db"},
		{name: "relative bare file name", url: "bolt://app.db", want: "app.db"},
		{name: "relative host only", url: "bolt://data", want: "data"},
		{name: "relative dot segment", url: "bolt://./data/app.db", want: "./data/app.db"},
		// An empty host keeps the absolute path untouched.
		{name: "absolute path", url: "bolt:///var/lib/app.db", want: "/var/lib/app.db"},
		{name: "absolute single segment", url: "bolt:///app.db", want: "/app.db"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := boltfx.DBPath(tt.url)
			if err != nil {
				t.Fatalf("DBPath(%q): %v", tt.url, err)
			}

			if got != tt.want {
				t.Errorf("DBPath(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestDBPathInvalid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want error
	}{
		{name: "empty url", url: "", want: boltfx.ErrEmptyURL},
		{name: "no scheme", url: "/var/lib/app.db", want: boltfx.ErrInvalidScheme},
		{name: "foreign scheme", url: "postgres://localhost/app", want: boltfx.ErrInvalidScheme},
		{name: "bare path scheme", url: "/app.db", want: boltfx.ErrInvalidScheme},
		{name: "empty path", url: "bolt://", want: boltfx.ErrEmptyURL},
		{name: "root path", url: "bolt:///", want: boltfx.ErrEmptyURL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := boltfx.DBPath(tt.url)
			if !errors.Is(err, tt.want) {
				t.Fatalf("DBPath(%q) error = %v, want %v", tt.url, err, tt.want)
			}

			if got != "" {
				t.Errorf("DBPath(%q) = %q, want empty path", tt.url, got)
			}
		})
	}
}

func TestNewAbsoluteURL(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "absolute.db")

	db, err := boltfx.New(boltfx.Config{URL: "bolt://" + path}, zap.NewNop())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	// An absolute URL keeps its leading slash: the file lands on disk exactly
	// where the URL pointed at.
	if got := db.Path(); got != path {
		t.Errorf("db.Path() = %q, want %q", got, path)
	}
}

func TestNewOpenAndClose(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "round-trip.db")

	db, err := boltfx.New(boltfx.Config{URL: "bolt://" + path}, zap.NewNop())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	if got := db.Path(); got != path {
		t.Errorf("db.Path() = %q, want %q", got, path)
	}

	if err = db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	// The handle is closed: a write has to fail instead of silently working.
	if err = db.Update(func(_ *bolt.Tx) error { return nil }); !errors.Is(err, bolterrors.ErrDatabaseNotOpen) {
		t.Fatalf("update after close = %v, want %v", err, bolterrors.ErrDatabaseNotOpen)
	}
}

func TestNewInvalidURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		url  string
		want error
	}{
		{name: "empty url", url: "", want: boltfx.ErrEmptyURL},
		{name: "foreign scheme", url: "postgres://localhost/app", want: boltfx.ErrInvalidScheme},
		{name: "empty path", url: "bolt://", want: boltfx.ErrEmptyURL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			db, err := boltfx.New(boltfx.Config{URL: tt.url}, zap.NewNop())
			if !errors.Is(err, tt.want) {
				t.Fatalf("New(%q) error = %v, want %v", tt.url, err, tt.want)
			}

			if db != nil {
				t.Errorf("New(%q) returned a database, want nil", tt.url)

				if closeErr := db.Close(); closeErr != nil {
					t.Errorf("close database: %v", closeErr)
				}
			}
		})
	}
}

func TestNewOpenFailure(t *testing.T) {
	t.Parallel()

	// bbolt does not create parent directories, so a missing one is an open
	// failure rather than a silent success.
	url := "bolt://" + filepath.Join(t.TempDir(), "missing", "app.db")

	db, err := boltfx.New(boltfx.Config{URL: url}, zap.NewNop())
	if !errors.Is(err, boltfx.ErrOpenFailed) {
		t.Fatalf("New(%q) error = %v, want %v", url, err, boltfx.ErrOpenFailed)
	}

	if db != nil {
		t.Errorf("New(%q) returned a database, want nil", url)

		if closeErr := db.Close(); closeErr != nil {
			t.Errorf("close database: %v", closeErr)
		}
	}
}

func TestNewPersistence(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "persist.db")
	url := "bolt://" + path

	db, err := boltfx.New(boltfx.Config{URL: url}, zap.NewNop())
	if err != nil {
		t.Fatalf("open database: %v", err)
	}

	if err = db.Update(func(tx *bolt.Tx) error {
		bucket, createErr := tx.CreateBucket([]byte(testBucket))
		if createErr != nil {
			return createErr
		}

		return bucket.Put([]byte(testKey), []byte("payload"))
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err = db.Close(); err != nil {
		t.Fatalf("close database: %v", err)
	}

	db, err = boltfx.New(boltfx.Config{URL: url}, zap.NewNop())
	if err != nil {
		t.Fatalf("reopen database: %v", err)
	}
	defer db.Close()

	if err = db.View(func(tx *bolt.Tx) error {
		if got := tx.Bucket([]byte(testBucket)).Get([]byte(testKey)); string(got) != "payload" {
			t.Errorf("value = %q, want %q", got, "payload")
		}

		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

func TestModuleClosesDatabaseOnStop(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "module.db")
	ctx := context.Background()

	var db *bolt.DB

	app := fx.New(
		fx.NopLogger,
		boltfx.Module(),
		fx.Supply(boltfx.Config{URL: "bolt://" + path}),
		fx.Supply(zap.NewNop()),
		fx.Populate(&db),
	)

	if err := app.Start(ctx); err != nil {
		t.Fatalf("start app: %v", err)
	}

	if err := db.Update(func(tx *bolt.Tx) error {
		_, createErr := tx.CreateBucketIfNotExists([]byte(testBucket))

		return createErr
	}); err != nil {
		t.Fatalf("write while running: %v", err)
	}

	if err := app.Stop(ctx); err != nil {
		t.Fatalf("stop app: %v", err)
	}

	// The lifecycle hook owns the handle: after stop the database is closed.
	if err := db.Update(func(_ *bolt.Tx) error { return nil }); !errors.Is(err, bolterrors.ErrDatabaseNotOpen) {
		t.Fatalf("update after stop = %v, want %v", err, bolterrors.ErrDatabaseNotOpen)
	}
}

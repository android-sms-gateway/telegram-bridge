// Package boltfx provides an fx-managed bbolt (BoltDB) handle.
//
// It opens a single embedded key/value database file from a URL and ties its
// lifetime to the fx application lifecycle: the handle is provided as a
// *bbolt.DB and closed when the application stops.
//
// The package deliberately depends on nothing but the standard library, fx,
// zap, go-core-fx/logger and bbolt, and lives at pkg/boltfx so it can be
// extracted verbatim to github.com/go-core-fx/boltfx: add a go.mod with that
// module path, move the directory, tag v0.1.0 and switch the import path.
package boltfx

import (
	"fmt"
	"net/url"
	"os"
	"time"

	bolt "go.etcd.io/bbolt"
	"go.uber.org/zap"
)

const (
	// urlScheme is the only scheme accepted in Config.URL.
	urlScheme = "bolt"

	// openTimeout bounds the wait for the bbolt file lock. A database file
	// locked by another process fails fast instead of blocking startup.
	openTimeout = time.Second

	// fileMode is the permission of the database file: the data it holds is
	// service state, not world readable data.
	fileMode os.FileMode = 0o600

	// rootPath is the path of a URL that carries no file at all. URL paths are
	// always slash separated, on every platform.
	rootPath = "/"
)

// New opens the database described by config and returns the handle.
//
// The URL is parsed with [net/url.Parse] and must use the "bolt" scheme. A URL
// with a host (for example "bolt://data/app.db") yields a path relative to the
// working directory; a URL with an empty host ("bolt:///var/lib/app.db") yields
// an absolute path. An empty URL, a foreign scheme or a URL without a path is
// rejected before anything is opened.
//
// The caller owns the returned handle. Inside an fx application use [Module]
// instead, which closes the database on stop.
func New(config Config, logger *zap.Logger) (*bolt.DB, error) {
	path, err := dbPath(config.URL)
	if err != nil {
		return nil, err
	}

	logger.Debug("opening database", zap.String("path", path))

	db, err := bolt.Open(path, fileMode, &bolt.Options{
		Timeout:         openTimeout,
		NoGrowSync:      false,
		NoFreelistSync:  false,
		PreLoadFreelist: false,
		FreelistType:    bolt.FreelistArrayType,
		ReadOnly:        false,
		MmapFlags:       0,
		InitialMmapSize: 0,
		PageSize:        0,
		MaxSize:         0,
		NoSync:          false,
		OpenFile:        nil,
		Mlock:           false,
		Logger:          nil,
		NoStatistics:    false,
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrOpenFailed, path, err)
	}

	logger.Debug("database opened", zap.String("path", path))

	return db, nil
}

// dbPath converts a "bolt://" URL into the filesystem path bbolt opens.
//
// It returns ErrEmptyURL for an empty URL or a URL without a path and
// ErrInvalidScheme for anything that is not a parseable "bolt" URL.
func dbPath(rawURL string) (string, error) {
	if rawURL == "" {
		return "", ErrEmptyURL
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("%w: %s: %w", ErrInvalidScheme, rawURL, err)
	}

	if parsed.Scheme != urlScheme {
		return "", fmt.Errorf("%w: got %q, want %q", ErrInvalidScheme, parsed.Scheme, urlScheme)
	}

	// A relative URL has no leading slash, so net/url parses its first segment
	// as the host ("bolt://data/app.db" -> host "data", path "/app.db"). An
	// absolute URL has an empty host and its path already starts at the root.
	path := parsed.Path
	if parsed.Host != "" {
		path = parsed.Host + parsed.Path
	}

	if path == "" || path == rootPath {
		return "", fmt.Errorf("%w: no database path in %s", ErrEmptyURL, rawURL)
	}

	return path, nil
}

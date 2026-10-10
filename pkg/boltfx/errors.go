package boltfx

import "errors"

var (
	// ErrEmptyURL is returned when Config.URL carries no database location,
	// either because it is empty or because it holds no path at all (for
	// example "bolt://" or "bolt:///").
	ErrEmptyURL = errors.New("boltfx: url is empty")

	// ErrInvalidScheme is returned when Config.URL cannot be parsed or uses a
	// scheme other than "bolt".
	ErrInvalidScheme = errors.New("boltfx: invalid url scheme")

	// ErrOpenFailed is returned when the database file cannot be opened, for
	// example because it is locked by another process or because its parent
	// directory does not exist.
	ErrOpenFailed = errors.New("boltfx: open database")
)

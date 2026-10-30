package store

import "time"

// DefaultTTL is how long a mapping stays resolvable when Config.TTL carries no
// usable value.
//
// A mapping only has to outlive a human reading the forwarded bubble and
// replying to it; a week covers delayed replies without letting the bucket grow
// without bound. Expiry is enforced on read as well as by the cleanup loop, so a
// stale mapping can never send an SMS to the wrong number.
const DefaultTTL = 7 * 24 * time.Hour

// Config holds the configuration for the store module.
//
// The values are mapped in internal/config/module.go and consumed directly by
// this module's constructor. The database location is not part of it: that
// setting belongs to pkg/boltfx, which opens the file.
//
// Example:
//
//	cfg := store.Config{
//	    TTL: store.DefaultTTL,
//	}
//
//	repo, err := store.NewRepository(cfg, db, logger)
type Config struct {
	// TTL is how long a mapping written by Put stays resolvable. A zero or
	// negative value means DefaultTTL, so a mapping can never be created
	// already expired. Put applies it only when the caller left Mapping.ExpiresAt
	// empty.
	TTL time.Duration
}

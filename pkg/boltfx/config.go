package boltfx

// Config describes the database this module opens. It is always a struct,
// even though it currently holds a single setting, so that the module takes a
// named value through fx instead of a bare string.
type Config struct {
	// URL is the database location, for example "bolt://data/app.db" for a
	// relative path or "bolt:///var/lib/app.db" for an absolute one. The
	// scheme must be "bolt". The parent directory is not created: it has to
	// exist before New is called.
	URL string
}

package boltfx

// DBPath exposes the URL-to-path resolution used by New so tests can assert
// the relative and absolute URL forms without touching the working directory.
func DBPath(rawURL string) (string, error) {
	return dbPath(rawURL)
}

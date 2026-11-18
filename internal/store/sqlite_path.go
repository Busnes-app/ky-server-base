package store

import (
	"errors"
	"net/url"
	"path/filepath"
	"strings"
)

// SQLitePath resolves SQLite file-URI escapes exactly once, so storage, instance locks
// and backup checks name the same file. Plain filesystem paths keep literal percent signs.
func SQLitePath(dsn string) (string, error) {
	path, _, _ := strings.Cut(dsn, "?")
	if strings.HasPrefix(dsn, "file:") {
		u, err := url.Parse(dsn)
		if err != nil || (u.Host != "" && u.Host != "localhost") || u.User != nil {
			return "", errors.New("invalid SQLite file URI")
		}
		path = u.Path
		if u.Opaque != "" {
			path, err = url.PathUnescape(u.Opaque)
			if err != nil {
				return "", errors.New("invalid SQLite file URI")
			}
		}
		if u.Query().Get("mode") == "memory" {
			return "", errors.New("SQLite server requires a persistent file")
		}
	} else if strings.Contains(path, "://") {
		return "", errors.New("KY_DB_DSN is a server URL but the database driver is sqlite: set KY_DB_DRIVER=postgres for PostgreSQL (under compose, in docker-compose.yml or an overlay: it pins sqlite), or give a SQLite file path")
	}
	if path == "" || path == ":memory:" || strings.ContainsRune(path, 0) {
		return "", errors.New("SQLite server requires a persistent file")
	}
	return filepath.Clean(path), nil
}

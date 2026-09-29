package migrations

import (
	"context"
	"database/sql"
)

// RunThrough applies migrations up to and including version, to build an older schema.
func RunThrough(ctx context.Context, db *sql.DB, driver string, version int) error {
	full := registry
	defer func() { registry = full }()
	registry = nil
	for _, m := range full {
		if m.Version <= version {
			registry = append(registry, m)
		}
	}
	return Run(ctx, db, driver)
}

package migrations

import (
	"embed"

	"github.com/standards-lab/sqlate/migrate"
)

//go:embed postgres/*.sql
var files embed.FS

// Migrations returns the app's Postgres set in version order.
func Migrations() ([]migrate.Migration, error) {
	return migrate.Files(files, "postgres")
}

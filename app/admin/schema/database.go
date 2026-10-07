package schema

import (
	"context"
	"slices"

	blobfspostgres "github.com/standards-lab/blobfs/postgres"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/migrate"

	"github.com/JaimeStill/spike-s3-storage/app/migrations"
)

// NewMigrator builds sqlate's multi-set migrator over db for the two
// migration sets, bottom first: blobfs's set, under its own history table
// and the name its source exports, and then the app's set, named app,
// under sqlate's default table. It performs no I/O: the migrator validates
// the sets and opens nothing. Its Up, Reset, and Status are the schema
// operations as they stand; revert is the revert the migrator does not
// offer.
func NewMigrator(db *sqlate.DB) (*migrate.Migrator, error) {
	blobfsSet, err := blobfspostgres.Migrations()
	if err != nil {
		return nil, err
	}
	appSet, err := migrations.Migrations()
	if err != nil {
		return nil, err
	}
	return migrate.New(db, []migrate.Set{blobfsSet, {Name: "app", Migrations: appSet}}, migrate.Options{})
}

// revert reverts every applied migration of m's sets, the app's set first,
// so its foreign keys into blobfs's tables never block the revert. The
// history tables stay, where m's Reset drops them. Each set reverts in its
// own locked run, so a failure in blobfs's set leaves the app's set
// reverted.
func revert(ctx context.Context, m *migrate.Migrator) error {
	for _, l := range slices.Backward(m.Layers()) {
		if err := l.Down(ctx, len(l.Migrations())); err != nil {
			return err
		}
	}
	return nil
}

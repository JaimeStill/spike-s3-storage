//go:build integration

package app_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/standards-lab/go-core/process"
	godatabase "github.com/standards-lab/go-database"
	"github.com/standards-lab/go-database/postgres"

	"github.com/JaimeStill/spike-s3-storage/app/cli"
	"github.com/JaimeStill/spike-s3-storage/app/internal/app"
)

// The schema group against the compose stack's Postgres, through App.Run
// with the production graph, run by `mise run integration`, which starts its
// own isolated project and tears it down when the suite ends. The test
// resets the schema when it starts and when it ends, so it runs the same
// against a fresh stack and a reused one.

// schemaRun runs blobfs schema with args and fails the test unless it
// exits 0 with nothing on stderr. It returns stdout.
func schemaRun(t *testing.T, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	code := app.New(cli.Streams{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errOut}).Run(context.Background(), append([]string{"schema"}, args...))
	if code != process.ExitOK || errOut.Len() != 0 {
		t.Fatalf("schema %s: code = %d, stderr = %q", strings.Join(args, " "), code, errOut.String())
	}
	return out.String()
}

// historyTables reports whether each set's history table exists, read over
// a pool of the test's own.
func historyTables(t *testing.T) (blobfs, appTable bool) {
	t.Helper()
	var cfg godatabase.Config
	if err := cfg.Finalize("BLOBFS"); err != nil {
		t.Fatal(err)
	}
	db, err := postgres.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := db.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Shutdown(ctx) }()
	exists := func(table string) bool {
		var ok bool
		if err := db.Conn().QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", table).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	return exists("blobfs_schema_version"), exists("schema_version")
}

// The status tables: every set at its head, and every migration pending,
// which is how a set reads both after down, its history table empty, and
// after reset, its history table gone.
const (
	statusAtHead = "" +
		"set     table                  version  latest  pending  dirty\n" +
		"blobfs  blobfs_schema_version  3        3       none     false\n" +
		"app     schema_version         2        2       none     false\n"
	statusAllPending = "" +
		"set     table                  version  latest  pending                                  dirty\n" +
		"blobfs  blobfs_schema_version  0        3       1 directory, 2 file, 3 directory_status  false\n" +
		"app     schema_version         0        2       1 directory_owner, 2 bookmark            false\n"
)

func TestSchemaIntegration_UpStatusDownReset(t *testing.T) {
	schemaRun(t, "reset", "--yes")
	t.Cleanup(func() { schemaRun(t, "reset", "--yes") })

	if got, want := schemaRun(t, "up"), "schema up: both sets at head\n"; got != want {
		t.Errorf("up = %q, want %q", got, want)
	}
	if got := schemaRun(t, "status"); got != statusAtHead {
		t.Errorf("status after up =\n%s\nwant\n%s", got, statusAtHead)
	}

	if got, want := schemaRun(t, "down"), "schema down: both sets reverted\n"; got != want {
		t.Errorf("down = %q, want %q", got, want)
	}
	if got := schemaRun(t, "status"); got != statusAllPending {
		t.Errorf("status after down =\n%s\nwant\n%s", got, statusAllPending)
	}
	if blobfs, appTable := historyTables(t); !blobfs || !appTable {
		t.Errorf("after down, history tables exist = %t, %t; want both kept", blobfs, appTable)
	}

	if got, want := schemaRun(t, "reset", "--yes"), "schema reset: both sets reverted and their history tables dropped\n"; got != want {
		t.Errorf("reset = %q, want %q", got, want)
	}
	if got := schemaRun(t, "status"); got != statusAllPending {
		t.Errorf("status after reset =\n%s\nwant\n%s", got, statusAllPending)
	}
	if blobfs, appTable := historyTables(t); blobfs || appTable {
		t.Errorf("after reset, history tables exist = %t, %t; want both dropped", blobfs, appTable)
	}
}

package app_test

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/go-core/process"
	"github.com/standards-lab/sqlate"
	"github.com/standards-lab/sqlate/sqltest"
)

// Ownership and the bookmark commands driven through App.Run over
// buffers: their output over a scripted database, with the store and its
// configuration never built, and the usage errors a missing, malformed, or
// misapplied --unit makes before anything is built.

// resolved is the Postgres engine's path resolution reaching the
// top-level directory with id and name.
func resolved(id, name string) sqltest.Response {
	return sqltest.Response{
		Columns: append(slices.Clone(directoryColumns), "depth"),
		Rows:    [][]driver.Value{append(directoryRow(id, blobfs.RootID, name), int64(1))},
	}
}

// planRows is the lookup of plan.txt in /reports, available.
func planRows() sqltest.Response {
	return sqltest.Response{Columns: fileColumns, Rows: [][]driver.Value{planRow(reportsID)}}
}

// activeViolation is the violation Postgres raises for a second active
// bookmark of a unit, as sqlate's Postgres dialect maps it.
func activeViolation() error {
	return &sqlate.ConstraintError{
		Constraint: "uq_bookmark_active",
		Class:      sqlate.ErrUniqueViolation,
		Err:        errors.New("duplicate key value violates unique constraint"),
	}
}

func TestBookmarks_CommandsOverAScriptedDatabase(t *testing.T) {
	held := sqltest.Response{Columns: []string{"id"}, Rows: [][]driver.Value{{planID}}}
	tests := []struct {
		name      string
		args      []string
		responses []sqltest.Response
		want      string
	}{
		{
			name:      "mkdir --unit",
			args:      []string{"mkdir", "/reports", "--unit", strings.ToUpper(unitID)},
			responses: []sqltest.Response{resolvedRoot(), directoryRows(directoryRow(reportsID, blobfs.RootID, "reports")), {Affected: 1}},
			want:      "mkdir: /reports (id " + reportsID + ", unit " + unitID + ")\n",
		},
		{
			name: "ls / --unit",
			args: []string{"ls", "/", "--unit", unitID},
			responses: []sqltest.Response{
				sqltest.WithTotal(directoryRows(directoryRow(reportsID, blobfs.RootID, "reports")), 1),
			},
			want: "" +
				"KIND  NAME     SIZE  STATUS  UPDATED              ID\n" +
				"dir   reports  -     -       2026-10-06 12:00:00  " + reportsID + "\n" +
				"directories: 1 on page 1 of size 20, total 1\n" +
				"more: no\n" +
				"files: 0 on page 1 of size 20, total 0\n" +
				"more: no\n",
		},
		{
			name:      "bookmark add --active",
			args:      []string{"bookmark", "add", "/reports/plan.txt", "--unit", unitID, "--active"},
			responses: []sqltest.Response{resolved(reportsID, "reports"), planRows(), held, {Affected: 1}},
			want:      "bookmark add: /reports/plan.txt (file " + planID + ", unit " + unitID + ", active)\n",
		},
		{
			name: "bookmark add by id",
			args: []string{"bookmark", "add", "id:" + planID, "--unit", unitID},
			responses: []sqltest.Response{
				planRows(),
				ancestors([]driver.Value{reportsID, blobfs.RootID, "reports"}),
				held,
				{Affected: 1},
			},
			want: "bookmark add: /reports/plan.txt (file " + planID + ", unit " + unitID + ", inactive)\n",
		},
		{
			name: "ls by id --unit",
			args: []string{"ls", "id:" + reportsID, "--unit", unitID},
			responses: []sqltest.Response{
				directoryRows(directoryRow(reportsID, blobfs.RootID, "reports")),
				ancestors([]driver.Value{reportsID, blobfs.RootID, "reports"}),
				{Columns: []string{"n"}, Rows: [][]driver.Value{{int64(1)}}},
				sqltest.WithTotal(directoryRows(), 0),
				directoryRows(directoryRow(reportsID, blobfs.RootID, "reports")),
				sqltest.WithTotal(planRows(), 1),
				directoryRows(directoryRow(reportsID, blobfs.RootID, "reports")),
			},
			want: "" +
				"KIND  NAME      SIZE  STATUS     UPDATED              ID\n" +
				"file  plan.txt  12    available  2026-10-06 12:00:00  " + planID + "\n" +
				"directories: 0 on page 1 of size 20, total 0\n" +
				"more: no\n" +
				"files: 1 on page 1 of size 20, total 1\n" +
				"more: no\n",
		},
		{
			name: "bookmark ls",
			args: []string{"bookmark", "ls", "--unit", unitID},
			responses: []sqltest.Response{sqltest.WithTotal(sqltest.Response{
				Columns: []string{"file_id", "directory_id", "active", "path", "name", "status", "size", "content_type", "created_at", "updated_at"},
				Rows:    [][]driver.Value{{planID, reportsID, true, "/reports/plan.txt", "plan.txt", "available", int64(12), "text/plain", stamp, stamp}},
			}, 1)},
			want: "" +
				"PATH               SIZE  STATUS     ACTIVE  UPDATED\n" +
				"/reports/plan.txt  12    available  active  2026-10-06 12:00:00\n" +
				"bookmarks: 1 on page 1 of size 20, total 1\n" +
				"more: no\n",
		},
		{
			name:      "bookmark rm",
			args:      []string{"bookmark", "rm", "/reports/plan.txt", "--unit", unitID},
			responses: []sqltest.Response{resolved(reportsID, "reports"), planRows(), {Affected: 1}},
			want:      "bookmark rm: /reports/plan.txt (file " + planID + ", unit " + unitID + ")\n",
		},
		{
			name:      "bookmark rm by id",
			args:      []string{"bookmark", "rm", "id:" + planID, "--unit", unitID},
			responses: []sqltest.Response{planRows(), ancestors([]driver.Value{reportsID, blobfs.RootID, "reports"}), {Affected: 1}},
			want:      "bookmark rm: /reports/plan.txt (file " + planID + ", unit " + unitID + ")\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			a, built, rec := scriptedApp(t, &out, &errOut, tt.responses...)

			code := a.Run(context.Background(), tt.args)

			if code != process.ExitOK {
				t.Fatalf("code = %d, want %d; stderr = %q", code, process.ExitOK, errOut.String())
			}
			if out.String() != tt.want {
				t.Errorf("stdout =\n%s\nwant\n%s", out.String(), tt.want)
			}
			if n := rec.Pending(); n != 0 {
				t.Errorf("%d scripted responses unconsumed", n)
			}
			if got := built.Log(); !slices.Equal(got, scriptedBuilt) {
				t.Errorf("nodes built = %q, want %q: neither the store nor its configuration", got, scriptedBuilt)
			}
		})
	}
}

func TestBookmarks_ARefusalIsReportedOnce(t *testing.T) {
	var out, errOut bytes.Buffer
	held := sqltest.Response{Columns: []string{"id"}, Rows: [][]driver.Value{{planID}}}
	a, _, _ := scriptedApp(t, &out, &errOut, resolved(reportsID, "reports"), planRows(), held, sqltest.Response{Err: activeViolation()})

	code := a.Run(context.Background(), []string{"bookmark", "add", "/reports/plan.txt", "--unit", unitID, "--active"})

	if code != process.ExitFailure {
		t.Errorf("code = %d, want %d", code, process.ExitFailure)
	}
	if want := "blobfs bookmark add: files: add bookmark of /reports/plan.txt for unit " + unitID + ": the unit has an active bookmark already (constraint uq_bookmark_active)\n"; errOut.String() != want {
		t.Errorf("stderr = %q, want %q", errOut.String(), want)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want empty", out.String())
	}
}

func TestBookmarks_AMissingMalformedOrMisappliedUnitIsAUsageErrorThatBuildsNothing(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"bookmark add without --unit", []string{"bookmark", "add", "/reports/plan.txt", "--active"}, "required flag --unit not set"},
		{"bookmark ls without --unit", []string{"bookmark", "ls"}, "required flag --unit not set"},
		{"bookmark rm without --unit", []string{"bookmark", "rm", "/reports/plan.txt"}, "required flag --unit not set"},
		{"bookmark add with a malformed unit", []string{"bookmark", "add", "/reports/plan.txt", "--unit", "nope"}, `--unit "nope" is not a UUID`},
		{"bookmark ls with a malformed sort", []string{"bookmark", "ls", "--unit", unitID, "--sort", "path:up"}, "the direction is asc or desc"},
		{"mkdir with a malformed unit", []string{"mkdir", "/reports", "--unit", "nope"}, `--unit "nope" is not a UUID`},
		{"ls with a malformed unit", []string{"ls", "/", "--unit", "nope"}, `--unit "nope" is not a UUID`},
		{"mkdir --unit below the top level", []string{"mkdir", "/reports/2026", "--unit", unitID}, "ownership applies to a top-level directory only; give --unit with a top-level path only"},
		{"ls / --unit after a cursor", []string{"ls", "/", "--unit", unitID, "--after-dirs", "c"}, "the owner listing pages by number only; ls / --unit takes no --after-dirs or --after-files"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			var out, errOut bytes.Buffer
			a, built := haltedApp(&out, &errOut)

			code := a.Run(context.Background(), tt.args)

			if code != process.ExitUsage {
				t.Errorf("code = %d, want %d; stderr = %q", code, process.ExitUsage, errOut.String())
			}
			usage := "Usage: " + commandPath(tt.args) + " [flags]"
			if !strings.Contains(errOut.String(), tt.want) || !strings.Contains(errOut.String(), usage) {
				t.Errorf("stderr = %q, want %q and %q", errOut.String(), tt.want, usage)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want empty", out.String())
			}
			if got := built.Log(); len(got) != 0 {
				t.Errorf("nodes built = %q, want none", got)
			}
		})
	}
}

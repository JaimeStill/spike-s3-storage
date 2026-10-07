package files_test

import (
	"bytes"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/standards-lab/blobfs"
	"github.com/standards-lab/go-core/process"
	"github.com/standards-lab/sqlate/sqltest"

	"github.com/JaimeStill/spike-s3-storage/app/domain/files"
)

func TestWriteContents_WritesTheEntriesAndEachHalfsPage(t *testing.T) {
	updated := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	size := int64(12)
	dir := blobfs.Directory{ID: "D", Name: "reports", UpdatedAt: updated}
	file := blobfs.File{ID: "F", Name: "plan.txt", Size: &size, Status: "available", UpdatedAt: updated}
	tests := []struct {
		name    string
		listing files.Listing
		dirs    files.Page[blobfs.Directory]
		files   files.Page[blobfs.File]
		cursors bool
		want    string
	}{
		{
			name:    "counted pages",
			listing: files.Listing{Page: 1, Size: 20, Total: files.TotalExact},
			dirs:    files.Page[blobfs.Directory]{Total: 1},
			files:   files.Page[blobfs.File]{Total: 3, More: true},
			want: "directories: 1 on page 1 of size 20, total 1\nmore: no\n" +
				"files: 1 on page 1 of size 20, total 3\nmore: yes\n",
		},
		{
			name:    "an empty later page",
			listing: files.Listing{Page: 5, Size: 20, Total: files.TotalExact},
			dirs:    files.Page[blobfs.Directory]{Total: files.NoTotal},
			files:   files.Page[blobfs.File]{Total: files.NoTotal},
			want: "directories: 1 on page 5 of size 20, total unknown (the page is empty)\nmore: no\n" +
				"files: 1 on page 5 of size 20, total unknown (the page is empty)\nmore: no\n",
		},
		{
			name:    "not counted",
			listing: files.Listing{Page: 5, Size: 20, Total: files.TotalNone},
			dirs:    files.Page[blobfs.Directory]{Total: files.NoTotal},
			files:   files.Page[blobfs.File]{Total: files.NoTotal},
			want: "directories: 1 on page 5 of size 20, total not counted\nmore: no\n" +
				"files: 1 on page 5 of size 20, total not counted\nmore: no\n",
		},
		{
			name:    "cursors",
			listing: files.Listing{Page: 1, Size: 1, Total: files.TotalExact, After: files.After{Files: "f1"}},
			dirs:    files.Page[blobfs.Directory]{Total: 2, More: true, Next: "d1"},
			files:   files.Page[blobfs.File]{Total: files.NoTotal, More: true, Next: "f2"},
			cursors: true,
			want: "directories: 1 on page 1 of size 1, total 2\nmore: yes\nnext-dirs: d1\n" +
				"files: 1 after the cursor, size 1, total not counted\nmore: yes\nnext-files: f2\n",
		},
		{
			name:    "cursors left out",
			listing: files.Listing{Page: 1, Size: 1, Total: files.TotalExact, After: files.After{Files: "f1"}},
			dirs:    files.Page[blobfs.Directory]{Total: 2, More: true, Next: "d1"},
			files:   files.Page[blobfs.File]{Total: files.NoTotal, More: true, Next: "f2"},
			want: "directories: 1 on page 1 of size 1, total 2\nmore: yes\n" +
				"files: 1 after the cursor, size 1, total not counted\nmore: yes\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b bytes.Buffer
			c := files.Contents{Directories: tt.dirs, Files: tt.files}
			c.Directories.Rows = []blobfs.Directory{dir}
			c.Files.Rows = []blobfs.File{file}

			if err := files.WriteContents(&b, tt.listing, c, tt.cursors); err != nil {
				t.Fatal(err)
			}

			want := "KIND  NAME      SIZE  STATUS     UPDATED              ID\n" +
				"dir   reports   -     -          2026-10-06 12:00:00  D\n" +
				"file  plan.txt  12    available  2026-10-06 12:00:00  F\n" + tt.want
			if b.String() != want {
				t.Errorf("output =\n%s\nwant\n%s", b.String(), want)
			}
		})
	}
}

func TestWriteContents_NoEntriesWritesTheHeader(t *testing.T) {
	var b bytes.Buffer

	if err := files.WriteContents(&b, files.Listing{Page: 1, Size: 20, Total: files.TotalExact}, files.Contents{}, true); err != nil {
		t.Fatal(err)
	}

	want := "KIND  NAME  SIZE  STATUS  UPDATED  ID\n" +
		"directories: 0 on page 1 of size 20, total 0\nmore: no\n" +
		"files: 0 on page 1 of size 20, total 0\nmore: no\n"
	if b.String() != want {
		t.Errorf("output =\n%s\nwant\n%s", b.String(), want)
	}
}

func TestBookmarkLs_WritesTheEntriesThenThePage(t *testing.T) {
	// The read model's page carries no cursor, and the listing writes
	// none.
	size := int64(12)
	r := runCommand(t, []sqltest.Response{sqltest.WithTotal(sqltest.Response{Columns: bookmarkColumns, Rows: [][]driver.Value{
		{fileID, dirID, true, "/reports/plan.txt", "plan.txt", "available", size, "text/plain", stamp, stamp},
		{otherID, blobfs.RootID, false, "/draft.bin", "draft.bin", "pending", nil, "application/octet-stream", stamp, stamp},
	}}, 2)}, "bookmark", "ls", "--unit", unitID)

	if r.code != process.ExitOK {
		t.Fatalf("code = %d, stderr = %q", r.code, r.stderr)
	}
	want := "" +
		"PATH               SIZE  STATUS     ACTIVE  UPDATED\n" +
		"/reports/plan.txt  12    available  active  2026-10-06 12:00:00\n" +
		"/draft.bin         -     pending    -       2026-10-06 12:00:00\n" +
		"bookmarks: 2 on page 1 of size 20, total 2\n" +
		"more: no\n"
	if r.stdout != want {
		t.Errorf("bookmark ls wrote\n%s\nwant\n%s", r.stdout, want)
	}
}

func TestBookmarkLs_NoEntriesWritesTheHeader(t *testing.T) {
	r := runCommand(t, []sqltest.Response{{Columns: bookmarkColumns}}, "bookmark", "ls", "--unit", unitID, "--total", "none")

	if r.code != process.ExitOK {
		t.Fatalf("code = %d, stderr = %q", r.code, r.stderr)
	}
	want := "PATH  SIZE  STATUS  ACTIVE  UPDATED\nbookmarks: 0 on page 1 of size 20, total not counted\nmore: no\n"
	if r.stdout != want {
		t.Errorf("bookmark ls wrote\n%q\nwant\n%q", r.stdout, want)
	}
}

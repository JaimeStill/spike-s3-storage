package files

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/standards-lab/blobfs"

	"github.com/JaimeStill/spike-s3-storage/app/output"
)

// WriteContents writes c, the contents l listed, as ls prints it: the
// entries as aligned columns under KIND NAME SIZE STATUS UPDATED ID, each
// directory and then each file in the order the page holds them, with the
// id last so the name stays second; then each half's page, as writePage
// writes it. With cursors, each half's lines end with the cursor that
// continues it; without, no cursor line is written.
func WriteContents(w io.Writer, l Listing, c Contents, cursors bool) error {
	rows := make([][]string, 0, len(c.Directories.Rows)+len(c.Files.Rows))
	for _, d := range c.Directories.Rows {
		rows = append(rows, []string{"dir", d.Name, "-", "-", d.UpdatedAt.UTC().Format(time.DateTime), d.ID})
	}
	for _, f := range c.Files.Rows {
		status := "-"
		if f.Status != "" {
			status = string(f.Status)
		}
		rows = append(rows, []string{"file", f.Name, sizeCell(f.Size), status, f.UpdatedAt.UTC().Format(time.DateTime), f.ID})
	}
	if err := output.Table(w, []string{"KIND", "NAME", "SIZE", "STATUS", "UPDATED", "ID"}, rows); err != nil {
		return err
	}
	dirs, files := pageOf(l, l.After.Directories, c.Directories), pageOf(l, l.After.Files, c.Files)
	if !cursors {
		dirs.next, files.next = "", ""
	}
	if err := writePage(w, "directories", "next-dirs", dirs); err != nil {
		return err
	}
	return writePage(w, "files", "next-files", files)
}

// writeBookmarks writes p, the page of a unit's bookmarks l listed, as
// bookmark ls prints it: the bookmarks as aligned columns under PATH SIZE
// STATUS ACTIVE UPDATED, in the order the page holds them, the active one
// marked active, then the page's lines as writePage writes them. The
// listing pages by number only, so no cursor line is written.
func writeBookmarks(w io.Writer, l Listing, p Page[Bookmark]) error {
	rows := make([][]string, 0, len(p.Rows))
	for _, b := range p.Rows {
		active := "-"
		if b.Active {
			active = "active"
		}
		rows = append(rows, []string{b.Path, sizeCell(b.Size), string(b.Status), active, b.UpdatedAt.UTC().Format(time.DateTime)})
	}
	if err := output.Table(w, []string{"PATH", "SIZE", "STATUS", "ACTIVE", "UPDATED"}, rows); err != nil {
		return err
	}
	s := pageOf(l, "", p)
	s.next = ""
	return writePage(w, "bookmarks", "", s)
}

// writeFileRecord writes a file's row as stat prints it, one field per
// line, led by the path it is at.
func writeFileRecord(w io.Writer, path string, f blobfs.File) error {
	return output.Record(w, fileRecord(path, f))
}

// WriteDirectoryRecord writes a directory's row as stat prints it, one
// field per line, led by the path it is at.
func WriteDirectoryRecord(w io.Writer, path string, d blobfs.Directory) error {
	return output.Record(w, directoryRecord(path, d))
}

// fileRecord lays a file row out as the fields stat prints, in order, the
// path first.
func fileRecord(path string, f blobfs.File) []output.Field {
	return []output.Field{
		{Name: "path", Value: path},
		{Name: "id", Value: f.ID},
		{Name: "name", Value: f.Name},
		{Name: "status", Value: string(f.Status)},
		{Name: "size", Value: sizeCell(f.Size)},
		{Name: "content-type", Value: f.ContentType},
		{Name: "etag", Value: etagOf(f)},
		{Name: "key", Value: f.Key},
		{Name: "version", Value: strconv.FormatInt(f.Version, 10)},
		{Name: "created", Value: f.CreatedAt.UTC().Format(time.RFC3339)},
		{Name: "updated", Value: f.UpdatedAt.UTC().Format(time.RFC3339)},
	}
}

// directoryRecord lays a directory row out as the fields stat prints, in
// the file record's order for the fields the two share, the path first;
// the parent is - for the root.
func directoryRecord(path string, d blobfs.Directory) []output.Field {
	parent := "-"
	if d.ParentID != nil {
		parent = *d.ParentID
	}
	return []output.Field{
		{Name: "path", Value: path},
		{Name: "id", Value: d.ID},
		{Name: "parent", Value: parent},
		{Name: "name", Value: d.Name},
		{Name: "version", Value: strconv.FormatInt(d.Version, 10)},
		{Name: "created", Value: d.CreatedAt.UTC().Format(time.RFC3339)},
		{Name: "updated", Value: d.UpdatedAt.UTC().Format(time.RFC3339)},
	}
}

// sizeOf returns a file's size, or 0 when the row records none.
func sizeOf(f blobfs.File) int64 {
	if f.Size == nil {
		return 0
	}
	return *f.Size
}

// etagOf returns a file's etag, or - when the row records none.
func etagOf(f blobfs.File) string {
	if f.ETag == nil {
		return "-"
	}
	return *f.ETag
}

// sizeCell renders a row's size, or - when the row records none.
func sizeCell(size *int64) string {
	if size == nil {
		return "-"
	}
	return strconv.FormatInt(*size, 10)
}

// pageSummary is what one half's page lines say: the page number and size
// the listing asked for, how many rows the page holds, and its total,
// which is NoTotal when the page carries none and is not reported at all
// when the listing did not count it (counted false). cursor says the half
// was read after a cursor rather than by number, so the number is not
// shown. more says whether rows remain after the page, and next is the
// cursor of the following page, written on its own line when it is not
// empty.
type pageSummary struct {
	number, size, listed, total int
	counted, cursor, more       bool
	next                        string
}

// pageOf summarizes one half's page: the request's page and size, whether
// the half was read after a cursor (after not empty), the rows listed, the
// total as the half reported it, marked counted when the listing asked for
// one and the half was read by number, whether rows remain, and the cursor
// of the next page.
func pageOf[T any](l Listing, after string, p Page[T]) pageSummary {
	return pageSummary{
		number: l.Page, size: l.Size, listed: len(p.Rows), total: p.Total,
		counted: l.Total == TotalExact && after == "", cursor: after != "", more: p.More, next: p.Next,
	}
}

// writePage writes one half's lines: the rows on the page, the page number
// and size (or the cursor and the size), and the total as counted, not
// counted, or unknown; then more: yes or more: no; then the next cursor
// under label when there is one.
func writePage(w io.Writer, half, label string, p pageSummary) error {
	total := "not counted"
	switch {
	case p.counted && p.total == NoTotal:
		total = "unknown (the page is empty)"
	case p.counted:
		total = strconv.Itoa(p.total)
	}
	var err error
	if p.cursor {
		_, err = fmt.Fprintf(w, "%s: %d after the cursor, size %d, total %s\n", half, p.listed, p.size, total)
	} else {
		_, err = fmt.Fprintf(w, "%s: %d on page %d of size %d, total %s\n", half, p.listed, p.number, p.size, total)
	}
	if err != nil {
		return err
	}
	more := "no"
	if p.more {
		more = "yes"
	}
	if _, err := fmt.Fprintf(w, "more: %s\n", more); err != nil {
		return err
	}
	if p.next != "" {
		_, err = fmt.Fprintf(w, "%s: %s\n", label, p.next)
	}
	return err
}

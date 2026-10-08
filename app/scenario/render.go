package scenario

import (
	"io"

	"github.com/standards-lab/blobfs"

	"github.com/JaimeStill/spike-s3-storage/app/domain/files"
)

// showListing shows c as ls prints it under l, cursor lines included, so a
// later step can continue after a cursor the reader has seen.
func showListing(r *Reporter, l files.Listing, c files.Contents) error {
	return r.Show(func(w io.Writer) error { return files.WriteContents(w, l, c, true) })
}

// showDirectory shows a directory's row as stat prints it.
func showDirectory(r *Reporter, path string, d blobfs.Directory) error {
	return r.Show(func(w io.Writer) error { return files.WriteDirectoryRecord(w, path, d) })
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

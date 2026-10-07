package s3

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/standards-lab/go-storage"
)

// The object operations are not built yet. Each returns an error matching
// errors.ErrUnsupported, so a caller can tell the gap from a service
// failure.

// Put is not implemented yet.
func (c *Client) Put(context.Context, string, io.Reader, storage.PutOptions) (storage.Object, error) {
	return storage.Object{}, notImplemented("Put")
}

// Get is not implemented yet.
func (c *Client) Get(context.Context, string, storage.GetOptions) (storage.Blob, error) {
	return storage.Blob{}, notImplemented("Get")
}

// Stat is not implemented yet.
func (c *Client) Stat(context.Context, string) (storage.Object, error) {
	return storage.Object{}, notImplemented("Stat")
}

// Delete is not implemented yet.
func (c *Client) Delete(context.Context, string) error {
	return notImplemented("Delete")
}

// List is not implemented yet.
func (c *Client) List(context.Context, storage.ListOptions) (storage.Page, error) {
	return storage.Page{}, notImplemented("List")
}

func notImplemented(op string) error {
	return fmt.Errorf("s3: %s not implemented: %w", op, errors.ErrUnsupported)
}

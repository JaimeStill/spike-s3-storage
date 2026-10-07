package s3

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/standards-lab/go-storage"
)

// defaultContentType is what Put sends and reports when opts.ContentType is
// empty, the type S3 stores for an object written without one.
const defaultContentType = "application/octet-stream"

// maxSinglePartSize is the largest body Put sends as one PutObject. Put
// buffers the body in memory up to it before sending anything.
//
// TODO(slice 4): a body past this size goes up as a multipart upload
// through feature/s3/transfermanager; until then Put refuses it with an
// error matching errors.ErrUnsupported and stores nothing.
const maxSinglePartSize = 8 << 20

// Put reads body to its end into memory and only then sends one PutObject,
// so a failure of body, a Size mismatch included, sends nothing and leaves
// any object at key unchanged. The buffered body is seekable, which the
// SDK needs to sign a request over plain HTTP. A failure of body is
// returned wrapped and unclassified.
//
// PutObject's answer carries the ETag but no Last-Modified, so a
// HeadObject follows it for the ModifiedAt the other operations report.
// When that HeadObject fails, or reports another ETag because a concurrent
// writer replaced the object, the object is still written: Put succeeds and
// takes ModifiedAt from the PutObject answer's Date header instead.
func (c *Client) Put(ctx context.Context, key string, body io.Reader, opts storage.PutOptions) (storage.Object, error) {
	if err := validateKey(key); err != nil {
		return storage.Object{}, err
	}
	data, err := readBody(body, opts.Size)
	if err != nil {
		return storage.Object{}, err
	}
	contentType := opts.ContentType
	if contentType == "" {
		contentType = defaultContentType
	}

	out, err := c.s3.PutObject(ctx, &awss3.PutObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return storage.Object{}, classify(err)
	}

	obj := storage.Object{
		Key:         key,
		Size:        int64(len(data)),
		ContentType: contentType,
		ETag:        entityTag(out.ETag),
	}
	head, headErr := c.s3.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	if headErr == nil && entityTag(head.ETag) == obj.ETag {
		obj.ModifiedAt = aws.ToTime(head.LastModified)
	} else if date, ok := awsmiddleware.GetServerTime(out.ResultMetadata); ok {
		obj.ModifiedAt = date
	}
	return obj, nil
}

// readBody reads body through EOF and returns its bytes. A size above 0 is
// the declared length: a body that ends short of it or runs past it is an
// error, the short one wrapping io.ErrUnexpectedEOF. A body longer than
// maxSinglePartSize is an error matching errors.ErrUnsupported.
func readBody(body io.Reader, size int64) ([]byte, error) {
	if size > maxSinglePartSize {
		return nil, tooLargeForSinglePart(size)
	}
	// One byte past the bound shows whether the body runs past it.
	bound := int64(maxSinglePartSize)
	if size > 0 {
		bound = size
	}
	var buf bytes.Buffer
	if size > 0 {
		buf.Grow(int(size))
	}
	n, err := buf.ReadFrom(io.LimitReader(body, bound+1))
	if err != nil {
		return nil, fmt.Errorf("s3: read body: %w", err)
	}
	switch {
	case size > 0 && n > size:
		return nil, fmt.Errorf("s3: read body: body is longer than the declared size (%d bytes)", size)
	case size > 0 && n < size:
		return nil, fmt.Errorf("s3: read body: body ended after %d bytes, short of the declared size (%d bytes): %w", n, size, io.ErrUnexpectedEOF)
	case n > maxSinglePartSize:
		return nil, tooLargeForSinglePart(n)
	}
	return buf.Bytes(), nil
}

// tooLargeForSinglePart reports a body Put cannot send as one PutObject.
func tooLargeForSinglePart(n int64) error {
	return fmt.Errorf("s3: body of %d bytes or more exceeds the single-part limit (%d bytes); multipart upload not implemented: %w",
		n, maxSinglePartSize, errors.ErrUnsupported)
}

// Get opens the object at key with one GetObject request. NoSuchKey is
// storage.ErrNotFound and NoSuchBucket storage.ErrContainerNotFound, as
// classify maps them; a read of the body that fails is classified the same
// way.
func (c *Client) Get(ctx context.Context, key string, _ storage.GetOptions) (storage.Blob, error) {
	if err := validateKey(key); err != nil {
		return storage.Blob{}, err
	}
	out, err := c.s3.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	if err != nil {
		return storage.Blob{}, classify(err)
	}
	return storage.Blob{
		Key:         key,
		Size:        aws.ToInt64(out.ContentLength),
		ContentType: aws.ToString(out.ContentType),
		ETag:        entityTag(out.ETag),
		ModifiedAt:  aws.ToTime(out.LastModified),
		Body:        classifiedBody{out.Body},
	}, nil
}

// classifiedBody classifies a Get body's read failures with classify and
// passes io.EOF through unchanged.
type classifiedBody struct{ io.ReadCloser }

func (b classifiedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		err = classify(err)
	}
	return n, err
}

// Stat reads the object's metadata with one HeadObject request. A HEAD
// answer carries no body, so a missing key and a missing bucket both arrive
// as a bare 404; a HeadBucket that follows it tells them apart. A missing
// bucket is storage.ErrContainerNotFound, an existing one makes the 404
// storage.ErrNotFound, and a HeadBucket that fails otherwise returns its
// own classified error, never storage.ErrNotFound.
func (c *Client) Stat(ctx context.Context, key string) (storage.Object, error) {
	if err := validateKey(key); err != nil {
		return storage.Object{}, err
	}
	out, err := c.s3.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	if err != nil {
		if errorCode(err) != "NotFound" {
			return storage.Object{}, classify(err)
		}
		if probeErr := c.Probe(ctx); probeErr != nil {
			return storage.Object{}, fmt.Errorf("s3: stat %q: object answered 404, bucket check: %w", key, probeErr)
		}
		return storage.Object{}, fmt.Errorf("%w: %w", storage.ErrNotFound, err)
	}
	return storage.Object{
		Key:         key,
		Size:        aws.ToInt64(out.ContentLength),
		ContentType: aws.ToString(out.ContentType),
		ETag:        entityTag(out.ETag),
		ModifiedAt:  aws.ToTime(out.LastModified),
	}, nil
}

// Delete removes the object at key with one DeleteObject request. S3
// answers a delete of a missing key with success; a gateway that answers
// NoSuchKey instead is treated as success too.
func (c *Client) Delete(ctx context.Context, key string) error {
	if err := validateKey(key); err != nil {
		return err
	}
	_, err := c.s3.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	if errorCode(err) == "NoSuchKey" {
		return nil
	}
	return classify(err)
}

// List fetches one page of the bucket's flat listing with one ListObjectsV2
// request. opts.Prefix, opts.Token, and opts.Limit map to its prefix,
// continuation token, and max-keys; a Limit past the int32 range is
// clamped, and S3 itself caps a page at 1,000 keys. Page.Next is the next
// continuation token while the listing is truncated.
func (c *Client) List(ctx context.Context, opts storage.ListOptions) (storage.Page, error) {
	in := &awss3.ListObjectsV2Input{Bucket: aws.String(c.bucket)}
	if opts.Prefix != "" {
		in.Prefix = aws.String(opts.Prefix)
	}
	if opts.Token != "" {
		in.ContinuationToken = aws.String(opts.Token)
	}
	if opts.Limit > 0 {
		in.MaxKeys = aws.Int32(int32(min(opts.Limit, math.MaxInt32)))
	}

	out, err := c.s3.ListObjectsV2(ctx, in)
	if err != nil {
		return storage.Page{}, classify(err)
	}

	var page storage.Page
	if aws.ToBool(out.IsTruncated) {
		page.Next = aws.ToString(out.NextContinuationToken)
	}
	page.Objects = make([]storage.Object, 0, len(out.Contents))
	for _, item := range out.Contents {
		if item.Key == nil {
			continue
		}
		page.Objects = append(page.Objects, storage.Object{
			Key:  *item.Key,
			Size: aws.ToInt64(item.Size),
			ETag: entityTag(item.ETag),
			// A listing's time is ISO 8601 and may carry milliseconds, where
			// the Last-Modified header Put, Get, and Stat read has whole
			// seconds; truncating keeps the four in agreement.
			ModifiedAt: aws.ToTime(item.LastModified).UTC().Truncate(time.Second),
		})
	}
	return page, nil
}

// entityTag returns the ETag the service sent in HTTP entity-tag form, or ""
// when the answer carried none: an unquoted value gains its quotes, and a
// quoted or W/"..." value is returned as it is.
func entityTag(e *string) string {
	s := aws.ToString(e)
	if s == "" || strings.HasPrefix(s, `"`) || strings.HasPrefix(s, `W/"`) {
		return s
	}
	return `"` + s + `"`
}

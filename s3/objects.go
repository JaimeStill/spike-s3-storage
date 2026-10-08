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
	"github.com/aws/aws-sdk-go-v2/feature/s3/transfermanager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	smithymiddleware "github.com/aws/smithy-go/middleware"

	"github.com/standards-lab/go-storage"
)

// defaultContentType is what Put sends and reports when opts.ContentType is
// empty, the type S3 stores for an object written without one.
const defaultContentType = "application/octet-stream"

// Put sends a body of at most one part as a single PutObject and a longer
// one as a multipart upload through transfermanager, so the object appears
// whole or not at all. It reads the body through a part and one byte more
// into memory to decide. A body that fits is sent from that buffer, which
// is seekable, as the SDK needs to sign a request over plain HTTP. A longer
// body streams on: transfermanager uploads it a part at a time, holding a
// few parts in memory, never the whole body.
//
// A failure of body, or a body shorter or longer than a declared Size,
// fails Put. Before the decision it has sent nothing; during a multipart
// upload, transfermanager aborts the upload, and Put sends one more
// AbortMultipartUpload for the same upload ID, because transfermanager
// drops the error of an abort that fails after another failure, and S3
// advises a repeat abort to free parts still in flight. A failure of body
// is returned wrapped and unclassified; any other failure is classified.
//
// Neither PutObject's answer nor CompleteMultipartUpload's carries a
// Last-Modified, so a HeadObject follows either for the ModifiedAt the
// other operations report. When that HeadObject fails, or reports another
// ETag because a concurrent writer replaced the object, the object is still
// written: Put succeeds and takes ModifiedAt from the write answer's Date
// header instead.
func (c *Client) Put(ctx context.Context, key string, body io.Reader, opts storage.PutOptions) (storage.Object, error) {
	if err := validateKey(key); err != nil {
		return storage.Object{}, err
	}
	contentType := opts.ContentType
	if contentType == "" {
		contentType = defaultContentType
	}

	src := &bodyReader{r: body, size: opts.Size}
	var buf bytes.Buffer
	if opts.Size > 0 {
		buf.Grow(int(min(opts.Size, c.partSize+1)))
	}
	// One byte past a part shows whether the body needs more than one.
	n, err := buf.ReadFrom(io.LimitReader(src, c.partSize+1))
	if err != nil {
		return storage.Object{}, fmt.Errorf("s3: read body: %w", err)
	}

	obj := storage.Object{Key: key, ContentType: contentType}
	var meta smithymiddleware.Metadata
	if n <= c.partSize {
		out, err := c.s3.PutObject(ctx, &awss3.PutObjectInput{
			Bucket:        aws.String(c.bucket),
			Key:           aws.String(key),
			Body:          bytes.NewReader(buf.Bytes()),
			ContentLength: aws.Int64(n),
			ContentType:   aws.String(contentType),
		})
		if err != nil {
			return storage.Object{}, classify(err)
		}
		obj.ETag, meta = entityTag(out.ETag), out.ResultMetadata
	} else {
		in := &transfermanager.UploadObjectInput{
			Bucket:      aws.String(c.bucket),
			Key:         aws.String(key),
			Body:        io.MultiReader(&buf, src),
			ContentType: aws.String(contentType),
		}
		if opts.Size > 0 {
			in.ContentLength = aws.Int64(opts.Size)
		}
		out, err := c.uploader.UploadObject(ctx, in)
		if err != nil {
			return storage.Object{}, c.failedUpload(ctx, key, src, err)
		}
		obj.ETag, meta = entityTag(out.ETag), out.ResultMetadata
	}
	obj.Size = src.n

	head, headErr := c.s3.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(c.bucket), Key: aws.String(key)})
	if headErr == nil && entityTag(head.ETag) == obj.ETag {
		obj.ModifiedAt = aws.ToTime(head.LastModified)
	} else if date, ok := awsmiddleware.GetServerTime(meta); ok {
		obj.ModifiedAt = date
	}
	return obj, nil
}

// failedUpload returns the error Put reports for a failed multipart upload,
// after it repeats the abort of the upload transfermanager started. The
// body's own failure is returned unclassified, as it is before any request;
// any other is classified. A repeat abort that fails is named in the error
// but never classifies it, since the upload's failure is what Put reports.
func (c *Client) failedUpload(ctx context.Context, key string, src *bodyReader, err error) error {
	var abortErr error
	if mpErr, ok := errors.AsType[transfermanager.MultipartUploadError](err); ok && mpErr.UploadID() != "" {
		abortErr = c.abortUpload(ctx, key, mpErr.UploadID())
	}
	if src.err != nil {
		err = fmt.Errorf("s3: read body: %w", src.err)
	} else {
		err = classify(err)
	}
	if abortErr != nil {
		return fmt.Errorf("%w (abort of multipart upload failed, parts may remain: %v)", err, abortErr)
	}
	return err
}

// abortUpload sends AbortMultipartUpload for uploadID on a context detached
// from the caller's, bounded by abortTimeout, so a cancelled Put still
// aborts. NoSuchUpload, the answer for an upload already aborted, is
// success.
func (c *Client) abortUpload(ctx context.Context, key, uploadID string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), abortTimeout)
	defer cancel()
	_, err := c.s3.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{
		Bucket:   aws.String(c.bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
	if err != nil && errorCode(err) != "NoSuchUpload" {
		return classify(err)
	}
	return nil
}

// bodyReader reads a Put body, counting its bytes and holding it to a
// declared size above 0: a body that ends short of the size fails with an
// error wrapping io.ErrUnexpectedEOF, and one that runs past it fails as
// soon as the byte past the size arrives. It keeps the first failure, the
// body's own or a size mismatch, in err, so Put can tell a body failure
// from a request's, and returns it again on every later Read.
type bodyReader struct {
	r    io.Reader
	size int64
	n    int64
	err  error
}

func (b *bodyReader) Read(p []byte) (int, error) {
	if b.err != nil {
		return 0, b.err
	}
	if b.size > 0 {
		if b.n >= b.size {
			// At the declared size, one more byte shows a longer body.
			var one [1]byte
			k, err := io.ReadAtLeast(b.r, one[:], 1)
			switch {
			case k > 0:
				b.err = fmt.Errorf("body is longer than the declared size (%d bytes)", b.size)
			case err == io.EOF:
				return 0, io.EOF
			default:
				b.err = err
			}
			return 0, b.err
		}
		if rest := b.size - b.n; int64(len(p)) > rest {
			p = p[:rest]
		}
	}
	k, err := b.r.Read(p)
	b.n += int64(k)
	if err == io.EOF && b.size > 0 && b.n < b.size {
		err = fmt.Errorf("body ended after %d bytes, short of the declared size (%d bytes): %w", b.n, b.size, io.ErrUnexpectedEOF)
	}
	if err != nil && err != io.EOF {
		b.err = err
	}
	return k, err
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

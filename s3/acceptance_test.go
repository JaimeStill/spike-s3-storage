package s3_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/standards-lab/go-storage"
	"github.com/standards-lab/go-storage/storagetest"
)

// endpointEnv names the S3 gateway URL the acceptance tests run against, for
// example http://127.0.0.1:8333 for the SeaweedFS harness. The tests skip
// when it is unset, so they never run on the unit tier. The gateway must
// accept the access key testAccount with the secret testKey.
const endpointEnv = "S3_TEST_ENDPOINT"

// acceptanceEndpoint returns the gateway URL the environment names, and
// skips the test when it is unset.
func acceptanceEndpoint(t *testing.T) string {
	t.Helper()
	endpoint := os.Getenv(endpointEnv)
	if endpoint == "" {
		t.Skipf("%s not set", endpointEnv)
	}
	return endpoint
}

// acceptanceConfig returns a finalized Config aimed at the endpoint the
// environment names, over a bucket that exists for this test alone: it does
// not exist when the test starts, and it is emptied and deleted when the
// test ends. It skips the test when the endpoint is unset.
func acceptanceConfig(t *testing.T) storage.Config {
	t.Helper()
	endpoint := acceptanceEndpoint(t)

	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("random bucket name: %v", err)
	}
	name := "acceptance-" + hex.EncodeToString(b[:])

	cfg := storage.Config{
		Endpoint:  endpoint,
		Container: name,
		Account:   testAccount,
		Key:       testKey,
	}
	if err := cfg.Finalize(""); err != nil {
		t.Fatalf("finalize config: %v", err)
	}

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		deleteBucket(ctx, rawClient(endpoint), name)
	})
	return cfg
}

// rawClient is an SDK client aimed at endpoint the way the provider aims
// its own, for the setup and inspection the provider's API does not offer.
func rawClient(endpoint string) *awss3.Client {
	return awss3.New(awss3.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider(testAccount, testKey, ""),
		BaseEndpoint: aws.String(endpoint),
		UsePathStyle: true,
	})
}

// deleteBucket deletes every object version and delete marker in bucket,
// so a versioned bucket empties too, then deletes the bucket. It ignores
// every failure, a missing bucket included: it is cleanup.
func deleteBucket(ctx context.Context, c *awss3.Client, bucket string) {
	pages := awss3.NewListObjectVersionsPaginator(c, &awss3.ListObjectVersionsInput{Bucket: aws.String(bucket)})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			break
		}
		for _, v := range page.Versions {
			_, _ = c.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(bucket), Key: v.Key, VersionId: v.VersionId})
		}
		for _, m := range page.DeleteMarkers {
			_, _ = c.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(bucket), Key: m.Key, VersionId: m.VersionId})
		}
	}
	_, _ = c.DeleteBucket(ctx, &awss3.DeleteBucketInput{Bucket: aws.String(bucket)})
}

// TestAcceptance_Conformance runs the conformance suite over the provider
// against a real gateway. The suite creates the bucket itself. Its 3 MiB
// bodies fit in one part at the default 8 MiB, so each goes up as one
// PutObject; the multipart tests below cover the longer path.
func TestAcceptance_Conformance(t *testing.T) {
	cfg := acceptanceConfig(t)
	storagetest.Run(t, func(t *testing.T) storage.Client {
		return newClient(t, cfg)
	})
}

// TestAcceptance_MissingContainer runs the missing-container check against a
// real gateway, over a bucket the test names and never creates.
func TestAcceptance_MissingContainer(t *testing.T) {
	cfg := acceptanceConfig(t)
	storagetest.RunMissingContainer(t, func(t *testing.T) storage.Client {
		return newClient(t, cfg)
	})
}

// TestAcceptance_StatMissingBucket proves that Stat over a bucket that does
// not exist reports storage.ErrContainerNotFound and never
// storage.ErrNotFound, though the gateway answers its HeadObject with the
// same bare 404 it gives a missing key in an existing bucket.
func TestAcceptance_StatMissingBucket(t *testing.T) {
	cfg := acceptanceConfig(t)
	client := newClient(t, cfg)

	_, err := client.Stat(t.Context(), "absent/key.txt")
	if !errors.Is(err, storage.ErrContainerNotFound) {
		t.Fatalf("Stat over a missing bucket = %v, want ErrContainerNotFound", err)
	}
	if errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Stat over a missing bucket = %v, want it not to match ErrNotFound", err)
	}
	t.Logf("Stat over a missing bucket: %v", err)
}

// TestAcceptance_StoreStart drives storage.Store over the provider against
// a gateway without the bucket: the bucket is missing before Start, and
// Start creates it, probes it, and reports ready.
func TestAcceptance_StoreStart(t *testing.T) {
	cfg := acceptanceConfig(t)
	client := newClient(t, cfg)
	ctx := t.Context()

	if err := client.Probe(ctx); !errors.Is(err, storage.ErrContainerNotFound) {
		t.Fatalf("Probe before Start = %v, want ErrContainerNotFound", err)
	}

	store := storage.New(client, cfg)
	if err := store.Start(ctx); err != nil {
		t.Fatalf("Start without the bucket: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	if !store.Ready() {
		t.Fatal("Ready() = false after Start, want true")
	}
	if err := store.Probe(ctx); err != nil {
		t.Fatalf("Store.Probe after Start = %v, want nil", err)
	}
	if _, err := rawClient(cfg.Endpoint).HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(cfg.Container)}); err != nil {
		t.Fatalf("HeadBucket after Start = %v, want the bucket to exist", err)
	}
}

// TestAcceptance_StoreObjects drives the object operations through
// storage.Store against the gateway and logs the ETag and ModifiedAt each
// one reports, so a run records what the gateway answered.
func TestAcceptance_StoreObjects(t *testing.T) {
	cfg := acceptanceConfig(t)
	store := storage.New(newClient(t, cfg), cfg)
	if err := store.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})

	ctx := t.Context()
	key := "store/hello.txt"
	content := []byte("hello through the store")
	put, err := store.Put(ctx, key, bytes.NewReader(content), storage.PutOptions{ContentType: "text/plain", Size: int64(len(content))})
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	t.Logf("Put:  ETag=%q ModifiedAt=%v", put.ETag, put.ModifiedAt)

	blob, err := store.Get(ctx, key, storage.GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	data, err := io.ReadAll(blob.Body)
	_ = blob.Body.Close()
	if err != nil {
		t.Fatalf("read Get body: %v", err)
	}
	t.Logf("Get:  ETag=%q ModifiedAt=%v ContentType=%q Size=%d", blob.ETag, blob.ModifiedAt, blob.ContentType, blob.Size)
	if !bytes.Equal(data, content) {
		t.Errorf("Get returned %q, want %q", data, content)
	}

	stat, err := store.Stat(ctx, key)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	t.Logf("Stat: ETag=%q ModifiedAt=%v ContentType=%q Size=%d", stat.ETag, stat.ModifiedAt, stat.ContentType, stat.Size)

	page, err := store.List(ctx, storage.ListOptions{Prefix: "store/"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, obj := range page.Objects {
		t.Logf("List: Key=%q ETag=%q ModifiedAt=%v Size=%d", obj.Key, obj.ETag, obj.ModifiedAt, obj.Size)
	}
	if len(page.Objects) != 1 || page.Objects[0].Key != key {
		t.Fatalf("List = %+v, want the one key %q", page.Objects, key)
	}

	for op, etag := range map[string]string{"Get": blob.ETag, "Stat": stat.ETag, "List": page.Objects[0].ETag} {
		if etag != put.ETag {
			t.Errorf("%s ETag = %q, want %q as Put reported", op, etag, put.ETag)
		}
	}
	for op, at := range map[string]time.Time{"Get": blob.ModifiedAt, "Stat": stat.ModifiedAt, "List": page.Objects[0].ModifiedAt} {
		if !at.Equal(put.ModifiedAt) {
			t.Errorf("%s ModifiedAt = %v, want %v as Put reported", op, at, put.ModifiedAt)
		}
	}

	for range 2 {
		if err := store.Delete(ctx, key); err != nil {
			t.Fatalf("Delete: %v", err)
		}
	}
	if _, err := store.Stat(ctx, key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Stat after Delete = %v, want ErrNotFound", err)
	}
}

// TestAcceptance_StoreStartUnreachable aims the provider at a port nothing
// listens on: Probe and Store.Start both fail with storage.ErrUnavailable,
// with the SDK's default retries in play, as a deployment would run.
func TestAcceptance_StoreStartUnreachable(t *testing.T) {
	acceptanceEndpoint(t)
	cfg := storage.Config{
		Endpoint:  closedEndpoint(t),
		Container: "unreachable",
		Account:   testAccount,
		Key:       testKey,
	}
	if err := cfg.Finalize(""); err != nil {
		t.Fatalf("finalize config: %v", err)
	}
	client := newClient(t, cfg)

	if err := client.Probe(t.Context()); !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("Probe against an unreachable endpoint = %v, want ErrUnavailable", err)
	}
	store := storage.New(client, cfg)
	err := store.Start(t.Context())
	if !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("Start against an unreachable endpoint = %v, want ErrUnavailable", err)
	}
	t.Logf("Start: %v", err)
	if store.Ready() {
		t.Error("Ready() = true after a failed Start, want false")
	}
}

// TestAcceptance_EnsureContainerExisting runs EnsureContainer over a bucket
// that already exists, holds an object, and has versioning enabled: it
// succeeds, twice, and leaves the object, the listing, and the versioning
// state as they were.
func TestAcceptance_EnsureContainerExisting(t *testing.T) {
	cfg := acceptanceConfig(t)
	raw := rawClient(cfg.Endpoint)
	ctx := t.Context()
	bucket := aws.String(cfg.Container)
	key := aws.String("keep/me.txt")

	if _, err := raw.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: bucket}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if _, err := raw.PutBucketVersioning(ctx, &awss3.PutBucketVersioningInput{
		Bucket:                  bucket,
		VersioningConfiguration: &types.VersioningConfiguration{Status: types.BucketVersioningStatusEnabled},
	}); err != nil {
		t.Fatalf("PutBucketVersioning: %v", err)
	}
	if _, err := raw.PutObject(ctx, &awss3.PutObjectInput{Bucket: bucket, Key: key, Body: strings.NewReader("kept")}); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	before, err := raw.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: bucket, Key: key})
	if err != nil {
		t.Fatalf("HeadObject before: %v", err)
	}

	client := newClient(t, cfg)
	for i := range 2 {
		if err := client.EnsureContainer(ctx); err != nil {
			t.Fatalf("EnsureContainer #%d over an existing bucket = %v, want nil", i+1, err)
		}
	}
	if err := client.Probe(ctx); err != nil {
		t.Fatalf("Probe after EnsureContainer = %v, want nil", err)
	}

	after, err := raw.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: bucket, Key: key})
	if err != nil {
		t.Fatalf("HeadObject after EnsureContainer = %v, want the object kept", err)
	}
	if aws.ToString(after.ETag) != aws.ToString(before.ETag) ||
		!aws.ToTime(after.LastModified).Equal(aws.ToTime(before.LastModified)) ||
		aws.ToString(after.VersionId) != aws.ToString(before.VersionId) {
		t.Errorf("object changed: before ETag=%q LastModified=%v VersionId=%q, after ETag=%q LastModified=%v VersionId=%q",
			aws.ToString(before.ETag), aws.ToTime(before.LastModified), aws.ToString(before.VersionId),
			aws.ToString(after.ETag), aws.ToTime(after.LastModified), aws.ToString(after.VersionId))
	}
	list, err := raw.ListObjectsV2(ctx, &awss3.ListObjectsV2Input{Bucket: bucket})
	if err != nil {
		t.Fatalf("ListObjectsV2: %v", err)
	}
	if len(list.Contents) != 1 || aws.ToString(list.Contents[0].Key) != aws.ToString(key) {
		t.Errorf("listing after EnsureContainer has %d objects, want only %q", len(list.Contents), aws.ToString(key))
	}
	versioning, err := raw.GetBucketVersioning(ctx, &awss3.GetBucketVersioningInput{Bucket: bucket})
	if err != nil {
		t.Fatalf("GetBucketVersioning: %v", err)
	}
	if versioning.Status != types.BucketVersioningStatusEnabled {
		t.Errorf("versioning after EnsureContainer = %q, want it still Enabled", versioning.Status)
	}
}

// acceptancePartSize is the part size the multipart acceptance tests
// configure, S3's 5 MiB minimum, so a 12 MiB body goes up in three parts.
const acceptancePartSize = 5 << 20

// multipartClient returns a provider client over cfg's bucket, created,
// with the part size set to acceptancePartSize.
func multipartClient(t *testing.T, cfg storage.Config) storage.Client {
	t.Helper()
	cfg.Options = map[string]string{"part_size": strconv.Itoa(acceptancePartSize)}
	client := newClient(t, cfg)
	if err := client.EnsureContainer(t.Context()); err != nil {
		t.Fatalf("EnsureContainer: %v", err)
	}
	return client
}

// randomBytes returns n random bytes, so each part's content differs.
func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random content: %v", err)
	}
	return b
}

// openUploads returns the multipart uploads the gateway lists as in
// progress for key, read with a raw ListMultipartUploads.
func openUploads(t *testing.T, raw *awss3.Client, bucket, key string) []types.MultipartUpload {
	t.Helper()
	out, err := raw.ListMultipartUploads(t.Context(), &awss3.ListMultipartUploadsInput{Bucket: aws.String(bucket), Prefix: aws.String(key)})
	if err != nil {
		t.Fatalf("ListMultipartUploads: %v", err)
	}
	var uploads []types.MultipartUpload
	for _, u := range out.Uploads {
		if aws.ToString(u.Key) == key {
			uploads = append(uploads, u)
		}
	}
	return uploads
}

// wantAbsent asserts key has no object: Stat answers ErrNotFound and List
// does not name it.
func wantAbsent(t *testing.T, client storage.Client, key string) {
	t.Helper()
	if _, err := client.Stat(t.Context(), key); !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Stat(%q) = %v, want ErrNotFound", key, err)
	}
	page, err := client.List(t.Context(), storage.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, obj := range page.Objects {
		if obj.Key == key {
			t.Errorf("List names %q (%+v), want it absent", key, obj)
		}
	}
}

// pausingBody reads r and pauses once, after its first at bytes: it closes
// reached and blocks until release closes, so a test can look at the
// gateway while a multipart upload is in progress.
type pausingBody struct {
	r       io.Reader
	at      int64
	reached chan struct{}
	release chan struct{}
	read    int64
	paused  bool
}

func (b *pausingBody) Read(p []byte) (int, error) {
	if !b.paused {
		if b.read >= b.at {
			close(b.reached)
			<-b.release
			b.paused = true
		} else if rest := b.at - b.read; int64(len(p)) > rest {
			p = p[:rest]
		}
	}
	k, err := b.r.Read(p)
	b.read += int64(k)
	return k, err
}

// TestAcceptance_MultipartInvisibleUntilComplete puts a 12 MiB body of
// unknown size at 5 MiB parts. The body pauses at 6 MiB, after Put has
// decided on a multipart upload, started it, and handed part 1 over: the
// gateway lists the upload in progress, while Stat answers ErrNotFound and
// List lacks the key. Released, Put completes with a multipart ETag, which
// Get, Stat, and List then report with the content.
func TestAcceptance_MultipartInvisibleUntilComplete(t *testing.T) {
	cfg := acceptanceConfig(t)
	client := multipartClient(t, cfg)
	raw := rawClient(cfg.Endpoint)
	key := "big/video.bin"
	content := randomBytes(t, 12<<20)

	body := &pausingBody{r: bytes.NewReader(content), at: 6 << 20, reached: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(body.release) }) }
	t.Cleanup(release)

	type result struct {
		obj storage.Object
		err error
	}
	done := make(chan result, 1)
	go func() {
		obj, err := client.Put(t.Context(), key, body, storage.PutOptions{ContentType: "video/mp4"})
		done <- result{obj, err}
	}()

	select {
	case <-body.reached:
	case res := <-done:
		t.Fatalf("Put returned before the body paused: %+v, %v", res.obj, res.err)
	case <-time.After(time.Minute):
		t.Fatal("body never reached the pause")
	}
	uploads := openUploads(t, raw, cfg.Container, key)
	if len(uploads) != 1 {
		t.Fatalf("ListMultipartUploads while paused lists %d uploads for %q, want 1 in progress", len(uploads), key)
	}
	t.Logf("in progress: UploadId=%q Initiated=%v", aws.ToString(uploads[0].UploadId), aws.ToTime(uploads[0].Initiated))
	wantAbsent(t, client, key)

	release()
	var res result
	select {
	case res = <-done:
	case <-time.After(time.Minute):
		t.Fatal("Put never returned after the release")
	}
	if res.err != nil {
		t.Fatalf("Put = %v, want nil", res.err)
	}
	put := res.obj
	t.Logf("Put:  ETag=%q ModifiedAt=%v Size=%d", put.ETag, put.ModifiedAt, put.Size)
	if !regexp.MustCompile(`^"[0-9a-f]{32}-3"$`).MatchString(put.ETag) {
		t.Errorf("Put ETag = %q, want a quoted multipart ETag of 3 parts", put.ETag)
	}
	if put.Size != int64(len(content)) || put.ContentType != "video/mp4" || put.ModifiedAt.IsZero() {
		t.Errorf("Put = %+v, want Size %d, ContentType video/mp4, and a ModifiedAt", put, len(content))
	}

	stat, err := client.Stat(t.Context(), key)
	if err != nil {
		t.Fatalf("Stat after Put: %v", err)
	}
	blob, err := client.Get(t.Context(), key, storage.GetOptions{})
	if err != nil {
		t.Fatalf("Get after Put: %v", err)
	}
	got, err := io.ReadAll(blob.Body)
	_ = blob.Body.Close()
	if err != nil {
		t.Fatalf("read Get body: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("Get returned %d bytes, want the %d put", len(got), len(content))
	}
	page, err := client.List(t.Context(), storage.ListOptions{Prefix: "big/"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(page.Objects) != 1 || page.Objects[0].Key != key {
		t.Fatalf("List = %+v, want the one key %q", page.Objects, key)
	}
	t.Logf("Stat: ETag=%q ModifiedAt=%v Size=%d ContentType=%q", stat.ETag, stat.ModifiedAt, stat.Size, stat.ContentType)
	t.Logf("List: ETag=%q ModifiedAt=%v Size=%d", page.Objects[0].ETag, page.Objects[0].ModifiedAt, page.Objects[0].Size)
	for op, obj := range map[string]storage.Object{"Get": {ETag: blob.ETag, ModifiedAt: blob.ModifiedAt, Size: blob.Size},
		"Stat": stat, "List": page.Objects[0]} {
		if obj.ETag != put.ETag || !obj.ModifiedAt.Equal(put.ModifiedAt) || obj.Size != put.Size {
			t.Errorf("%s ETag=%q ModifiedAt=%v Size=%d, want %q, %v, and %d as Put reported", op, obj.ETag, obj.ModifiedAt, obj.Size, put.ETag, put.ModifiedAt, put.Size)
		}
	}
	if uploads := openUploads(t, raw, cfg.Container, key); len(uploads) != 0 {
		t.Errorf("ListMultipartUploads after Put lists %d uploads for %q, want none", len(uploads), key)
	}
}

// TestAcceptance_MultipartFailureLeavesNothing fails a 12 MiB multipart Put
// partway in three ways: the body fails, the body ends short of its
// declared size, and the caller cancels. Each returns its failure and
// leaves no object and no open multipart upload for the key.
func TestAcceptance_MultipartFailureLeavesNothing(t *testing.T) {
	cfg := acceptanceConfig(t)
	client := multipartClient(t, cfg)
	raw := rawClient(cfg.Endpoint)
	broke := errors.New("source broke")

	cases := []struct {
		name string
		body func(t *testing.T, cancel context.CancelFunc) io.Reader
		size int64
		want func(error) bool
	}{
		{"body fails midway", func(t *testing.T, _ context.CancelFunc) io.Reader {
			return io.MultiReader(bytes.NewReader(randomBytes(t, 11<<20)), iotest.ErrReader(broke))
		}, 0, func(err error) bool { return errors.Is(err, broke) }},
		{"shorter than declared", func(t *testing.T, _ context.CancelFunc) io.Reader {
			return bytes.NewReader(randomBytes(t, 11<<20))
		}, 12 << 20, func(err error) bool { return errors.Is(err, io.ErrUnexpectedEOF) }},
		{"caller cancels", func(t *testing.T, cancel context.CancelFunc) io.Reader {
			return &cancelAfter{r: bytes.NewReader(randomBytes(t, 12<<20)), n: 11 << 20, cancel: cancel}
		}, 0, func(err error) bool { return errors.Is(err, context.Canceled) }},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := "failed/" + strconv.Itoa(i)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			_, err := client.Put(ctx, key, tc.body(t, cancel), storage.PutOptions{Size: tc.size})
			if err == nil || !tc.want(err) {
				t.Fatalf("Put = %v, want the failure", err)
			}
			t.Logf("Put: %v", err)
			wantAbsent(t, client, key)
			if uploads := openUploads(t, raw, cfg.Container, key); len(uploads) != 0 {
				t.Errorf("ListMultipartUploads after the failure lists %d uploads for %q, want none", len(uploads), key)
			}
		})
	}
}

//go:build integration

package integration_test

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/standards-lab/go-core/process/processtest"
)

// The large-body tests run the binary at the smallest part S3 allows, so a
// body of a few parts takes the provider's multipart path: Put sends a body
// of at most one part as one PutObject, and streams a longer one, of
// declared or unknown size, through a multipart upload.
const (
	partSize = 5 << 20
	// largeSize is three parts' worth less one byte: two full parts and a
	// short last one, so the multipart ETag ends "-3".
	largeSize = 3*partSize - 1
	// largeParts is the number of parts a body of largeSize uploads in.
	largeParts = 3
	// headSize is what an interrupted put's standard input carries before
	// the signal: one part and one MiB, so the provider has passed its
	// one-part buffer, started the multipart upload, and sent the first
	// part, while the second waits on standard input for bytes that never
	// come.
	headSize = partSize + 1<<20
)

// large returns tg running its stores at partSize parts.
func large(tg target) target {
	return tg.with("BLOBFS_STORAGE_OPTIONS_PART_SIZE=" + strconv.Itoa(partSize))
}

// body returns n bytes of seeded pseudo-random data, the same for one seed
// on every run, and none of it compressible or repeated.
func body(seed uint64, n int) []byte {
	b := make([]byte, n)
	r := rand.NewChaCha8([32]byte{byte(seed)})
	_, _ = r.Read(b)
	return b
}

// digest is the transcript's name for a body: its length and SHA-256.
func digest(b []byte) string {
	return fmt.Sprintf("%d bytes, sha256 %x", len(b), sha256.Sum256(b))
}

// rawStore is an S3 client of the test's own over the store the
// environment names, with the credential the binary uses. The binary is
// the suite's only surface for state, so the client writes nothing: it
// reads what blobfs cannot show, the multipart uploads open in a bucket and
// the objects in it, to prove which path a put took and what an
// interrupted one left behind.
type rawStore struct {
	client *awss3.Client
	bucket string
}

// raw returns a rawStore over tg's container.
func raw(t *testing.T, tg target) rawStore {
	t.Helper()
	endpoint := storeEndpoint(t)
	client := awss3.New(awss3.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider(os.Getenv("BLOBFS_STORAGE_ACCOUNT"), os.Getenv("BLOBFS_STORAGE_KEY"), ""),
		BaseEndpoint: aws.String(endpoint.String()),
		UsePathStyle: true,
	})
	return rawStore{client: client, bucket: tg.container}
}

// uploads returns the multipart uploads open in the bucket, in the order
// ListMultipartUploads gives them. The test's uploads fit one page.
func (s rawStore) uploads(t *testing.T) []types.MultipartUpload {
	t.Helper()
	out, err := s.client.ListMultipartUploads(t.Context(), &awss3.ListMultipartUploadsInput{Bucket: aws.String(s.bucket)})
	if err != nil {
		t.Fatalf("list multipart uploads in %s: %v", s.bucket, err)
	}
	return out.Uploads
}

// bucketUploads is uploads, but for a bucket the put has not yet created,
// which has none.
func (s rawStore) bucketUploads(t *testing.T) []types.MultipartUpload {
	t.Helper()
	if _, err := s.client.HeadBucket(t.Context(), &awss3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		return nil
	}
	return s.uploads(t)
}

// parts returns how many parts the open upload has received.
func (s rawStore) parts(t *testing.T, u types.MultipartUpload) int {
	t.Helper()
	out, err := s.client.ListParts(t.Context(), &awss3.ListPartsInput{Bucket: aws.String(s.bucket), Key: u.Key, UploadId: u.UploadId})
	if err != nil {
		t.Fatalf("list parts of upload %s: %v", aws.ToString(u.UploadId), err)
	}
	return len(out.Parts)
}

// objects returns the keys of the objects in the bucket. The test's
// objects fit one page.
func (s rawStore) objects(t *testing.T) []string {
	t.Helper()
	out, err := s.client.ListObjectsV2(t.Context(), &awss3.ListObjectsV2Input{Bucket: aws.String(s.bucket)})
	if err != nil {
		t.Fatalf("list objects in %s: %v", s.bucket, err)
	}
	keys := make([]string, 0, len(out.Contents))
	for _, o := range out.Contents {
		keys = append(keys, aws.ToString(o.Key))
	}
	return keys
}

// etag returns the ETag the store holds for key.
func (s rawStore) etag(t *testing.T, key string) string {
	t.Helper()
	out, err := s.client.HeadObject(t.Context(), &awss3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("head %s: %v", key, err)
	}
	return aws.ToString(out.ETag)
}

// logUploads logs the uploads open in the bucket, with when, and returns
// them.
func (s rawStore) logUploads(t *testing.T, when string) []types.MultipartUpload {
	t.Helper()
	open := s.uploads(t)
	ids := make([]string, 0, len(open))
	for _, u := range open {
		ids = append(ids, aws.ToString(u.Key)+" "+aws.ToString(u.UploadId))
	}
	t.Logf("open multipart uploads in %s %s: %d %v", s.bucket, when, len(open), ids)
	return open
}

// multipartETag matches an entity tag in the multipart form, the hex of
// the parts' digests then "-" and the count of parts.
var multipartETag = regexp.MustCompile(`^"[0-9a-f]+-([0-9]+)"$`)

// stored fails the test unless the file at path holds want: cat returns
// its bytes, its row is available at its size, and the store holds it
// under the row's key as a multipart object of largeParts parts, with no
// upload left open in the bucket.
func stored(t *testing.T, tg target, s rawStore, path string, want []byte) {
	t.Helper()
	p := start(t, tg, strings.NewReader(""), "$ blobfs cat "+path, "cat", path)
	p.quiet = true
	got, errOut, code := p.wait(t)
	if code != 0 || errOut != "" {
		t.Fatalf("cat %s exited %d: %s", path, code, errOut)
	}
	if sha256.Sum256([]byte(got)) != sha256.Sum256(want) {
		t.Errorf("cat %s = %s, want %s", path, digest([]byte(got)), digest(want))
	}
	out := ok(t, tg, "stat", path)
	if field(out, "status") != "available" || field(out, "size") != strconv.Itoa(len(want)) {
		t.Errorf("stat %s:\n%s\nwant available at %d bytes", path, out, len(want))
	}
	etag := s.etag(t, field(out, "key"))
	if m := multipartETag.FindStringSubmatch(etag); m == nil || m[1] != strconv.Itoa(largeParts) {
		t.Errorf("the object of %s has ETag %s, want the multipart form ending -%d", path, etag, largeParts)
	}
	if field(out, "etag") != etag {
		t.Errorf("stat %s etag = %s, the store's %s", path, field(out, "etag"), etag)
	}
	if open := s.logUploads(t, "after the put of "+path); len(open) != 0 {
		t.Errorf("%d multipart uploads open after the put of %s, want none", len(open), path)
	}
}

// TestALargePut puts a body of three parts from a local file, whose size
// the binary declares, and from standard input, whose size it cannot know:
// each is stored whole as a multipart object, cat returns the same bytes,
// and no upload is left open.
func TestALargePut(t *testing.T) {
	tg := large(open(t))
	ok(t, tg, "schema", "up")
	ok(t, tg, "mkdir", "/large")

	file := body(1, largeSize)
	local := filepath.Join(t.TempDir(), "file.bin")
	if err := os.WriteFile(local, file, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s holds %s", local, digest(file))
	if out := ok(t, tg, "put", local, "/large/file.bin"); !strings.Contains(out, fmt.Sprintf(", %d bytes, etag ", largeSize)) {
		t.Errorf("put of a local file stdout = %q", out)
	}
	s := raw(t, tg)
	stored(t, tg, s, "/large/file.bin", file)

	stdin := body(2, largeSize)
	p := start(t, tg, bytes.NewReader(stdin), "$ blobfs put - /large/stdin.bin  # stdin "+digest(stdin), "put", "-", "/large/stdin.bin")
	out, errOut, code := p.wait(t)
	if code != 0 || errOut != "" || !strings.Contains(out, fmt.Sprintf(", %d bytes, etag ", largeSize)) {
		t.Errorf("put - exited %d: stdout %q, stderr %q", code, out, errOut)
	}
	stored(t, tg, s, "/large/stdin.bin", stdin)
}

// midUpload starts put - of path against tg with its standard input on a
// pipe, writes headSize bytes of a body that never ends, and waits until
// the store shows the put's multipart upload open with its first part
// received, so the provider is past its one-part buffer and the put is
// waiting on standard input for the second part. It returns the running
// put and the open upload.
func midUpload(t *testing.T, tg target, s rawStore, path string) (*proc, types.MultipartUpload) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	head := body(3, headSize)
	p := start(t, tg, r, fmt.Sprintf("$ blobfs put - %s &  # stdin held open after %s", path, digest(head)), "put", "-", path)
	// The child holds its own copy of the read end.
	_ = r.Close()
	// A pipe holds far less than a part, so the write runs while the child
	// reads; it ends when the child has taken it, or fails when the child
	// exits or the test closes the write end. The wait below, not the
	// write, is what shows the put mid-upload.
	go func() { _, _ = w.Write(head) }()
	var upload types.MultipartUpload
	processtest.WaitFor(t, path+"'s multipart upload with a part received", func() bool {
		if p.ended() {
			return true
		}
		open := s.bucketUploads(t)
		if len(open) != 1 {
			return false
		}
		upload = open[0]
		return s.parts(t, upload) > 0
	})
	if p.ended() {
		t.Fatalf("%s exited mid-upload:\nstdout: %s\nstderr: %s", p.line, p.stdout(), p.errOut.String())
	}
	t.Logf("the store shows upload %s of %s open with its first part", aws.ToString(upload.UploadId), aws.ToString(upload.Key))
	return p, upload
}

// TestAnInterruptedLargePut sends SIGINT to a put - mid-upload: the put
// has sent its first part and waits on standard input for the second. It
// exits one, reporting the cancellation once, as TestAnInterruptedPut's
// put does, and leaves nothing behind: the provider aborts the upload, and
// blobfs abandons the write, removing the pending row.
func TestAnInterruptedLargePut(t *testing.T) {
	const path = "/interrupted.bin"
	tg := large(open(t))
	ok(t, tg, "schema", "up")
	s := raw(t, tg)

	p, _ := midUpload(t, tg, s, path)
	s.logUploads(t, "before the interrupt")
	p.interrupt(t, path)

	refused(t, tg, "not found", "stat", path)
	if keys := s.objects(t); len(keys) != 0 {
		t.Errorf("objects in the bucket after the interrupt = %v, want none", keys)
	}
	if open := s.logUploads(t, "after the interrupt"); len(open) != 0 {
		t.Errorf("%d multipart uploads open after the interrupt, want none: the provider aborts", len(open))
	}
}

// TestACrashedLargePut kills a put - mid-upload with SIGKILL, which it
// cannot catch: the pending row stays, and so does its multipart upload,
// which nothing was left running to abort. A later put of the same name
// resumes the pending row, under the same id and key, and stores the
// whole body. The crashed put's upload is a different upload, which
// neither blobfs nor the provider knows of: it stays open after the
// resumed put completes, and after the file's removal, holding its part,
// until a bucket lifecycle rule for incomplete multipart uploads or an
// explicit sweep aborts it. The test logs the open uploads at each step.
func TestACrashedLargePut(t *testing.T) {
	const path = "/crashed.bin"
	tg := large(open(t))
	ok(t, tg, "schema", "up")
	s := raw(t, tg)

	p, upload := midUpload(t, tg, s, path)
	p.crash(t)

	out := ok(t, tg, "stat", path)
	id, key := field(out, "id"), field(out, "key")
	if field(out, "status") != "pending" || key != aws.ToString(upload.Key) {
		t.Fatalf("stat after the crash:\n%s\nwant pending at the key of upload %s, %s", out, aws.ToString(upload.UploadId), aws.ToString(upload.Key))
	}
	if keys := s.objects(t); len(keys) != 0 {
		t.Errorf("objects in the bucket after the crash = %v, want none", keys)
	}
	if open := s.logUploads(t, "after the crash"); len(open) != 1 || aws.ToString(open[0].UploadId) != aws.ToString(upload.UploadId) {
		t.Fatalf("open uploads after the crash = %d, want the crashed put's alone", len(open))
	}

	// The resumed put stores the whole body under the pending row's id and
	// key, as a multipart object of its own upload.
	resumed := body(4, largeSize)
	local := filepath.Join(t.TempDir(), "resumed.bin")
	if err := os.WriteFile(local, resumed, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("%s holds %s", local, digest(resumed))
	if out := ok(t, tg, "put", local, path); !strings.HasPrefix(out, "put: "+path+" (id "+id+", ") || !strings.HasSuffix(out, ", resumed the pending row)\n") {
		t.Errorf("put onto the crashed put's pending row stdout = %q, want it resumed under id %s", out, id)
	}
	if out := ok(t, tg, "stat", path); field(out, "key") != key {
		t.Errorf("stat after the resumed put:\n%s\nwant the pending row's key %s", out, key)
	}
	p = start(t, tg, strings.NewReader(""), "$ blobfs cat "+path, "cat", path)
	p.quiet = true
	if got, errOut, code := p.wait(t); code != 0 || sha256.Sum256([]byte(got)) != sha256.Sum256(resumed) {
		t.Errorf("cat %s exited %d with %s, want %s: %s", path, code, digest([]byte(got)), digest(resumed), errOut)
	}
	etag := s.etag(t, key)
	if m := multipartETag.FindStringSubmatch(etag); m == nil || m[1] != strconv.Itoa(largeParts) {
		t.Errorf("the resumed object has ETag %s, want the multipart form ending -%d", etag, largeParts)
	}

	// The crashed put's upload is the orphan: still open, with its part,
	// after the resumed put completed the key, and after the file is gone.
	orphan := func(when string) {
		t.Helper()
		open := s.logUploads(t, when)
		if len(open) != 1 || aws.ToString(open[0].UploadId) != aws.ToString(upload.UploadId) {
			t.Errorf("open uploads %s = %d, want the crashed put's alone", when, len(open))
			return
		}
		t.Logf("the orphaned upload %s holds %d part(s)", aws.ToString(upload.UploadId), s.parts(t, open[0]))
	}
	orphan("after the resumed put")
	ok(t, tg, "rm", path)
	orphan("after rm " + path)
	if keys := s.objects(t); len(keys) != 0 {
		t.Errorf("objects in the bucket after rm = %v, want none", keys)
	}

	// Nothing in the test aborts it: the suite writes nothing to the store
	// behind the binary's back, and the compose project's volumes go with
	// the suite.
}

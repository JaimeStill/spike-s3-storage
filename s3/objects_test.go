package s3_test

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/aws/smithy-go"

	"github.com/standards-lab/go-storage"
)

// testKeyPath is the request path of the key "k" in the test bucket.
const testKeyPath = "/" + testBucket + "/k"

// lastModified is the Last-Modified the scripted service reports, in the
// header's whole-second form.
var lastModified = time.Date(2026, 10, 7, 12, 30, 45, 0, time.UTC)

// byRoute answers each request with the handler for its method and path,
// such as "HEAD /unit/k", and with 405 for a route it has none for.
func byRoute(handlers map[string]http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h, ok := handlers[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	}
}

// object answers with an object's headers and, on GET, its content.
func object(etag, contentType, content string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", fmt.Sprint(len(content)))
		w.Header().Set("Last-Modified", lastModified.Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, content)
		}
	}
}

// stored answers a PutObject with the ETag it stored under and no
// Last-Modified, as S3 does, and a Date header of date.
func stored(etag string, date time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", etag)
		w.Header().Set("Date", date.Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	}
}

// listing answers a ListObjectsV2 with body as the ListBucketResult's inner
// XML.
func listing(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Name>%s</Name>%s</ListBucketResult>`, testBucket, body)
	}
}

// wantOnly asserts err matches sentinel and none of the other storage
// sentinels a provider classifies into.
func wantOnly(t *testing.T, what string, err, sentinel error) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Fatalf("%s = %v, want %v", what, err, sentinel)
	}
	for _, other := range []error{storage.ErrNotFound, storage.ErrContainerNotFound, storage.ErrUnavailable} {
		if other != sentinel && errors.Is(err, other) {
			t.Fatalf("%s = %v, want it not to match %v", what, err, other)
		}
	}
	if _, ok := errors.AsType[smithy.APIError](err); !ok {
		t.Fatalf("%s = %v, want the SDK's APIError matchable", what, err)
	}
}

func TestPut_SendsOnePutObjectAndStatsForModifiedAt(t *testing.T) {
	svc := newService(t, byRoute(map[string]http.HandlerFunc{
		"PUT " + testKeyPath:  stored(`"abc"`, lastModified.Add(time.Second)),
		"HEAD " + testKeyPath: object(`"abc"`, "text/plain", "hello"),
	}))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	obj, err := c.Put(t.Context(), "k", strings.NewReader("hello"), storage.PutOptions{ContentType: "text/plain", Size: 5})
	if err != nil {
		t.Fatalf("Put = %v, want nil", err)
	}
	want := storage.Object{Key: "k", Size: 5, ContentType: "text/plain", ETag: `"abc"`, ModifiedAt: lastModified}
	if obj != want {
		t.Errorf("Put = %+v, want %+v", obj, want)
	}
	reqs := svc.Requests()
	if len(reqs) != 2 || reqs[0].Method != http.MethodPut || reqs[1].Method != http.MethodHead {
		t.Fatalf("service saw %+v, want a PUT then a HEAD", reqs)
	}
	if got := string(reqs[0].Body); got != "hello" {
		t.Errorf("PutObject body = %q, want %q", got, "hello")
	}
	if got := reqs[0].Header.Get("Content-Type"); got != "text/plain" {
		t.Errorf("PutObject Content-Type = %q, want text/plain", got)
	}
}

func TestPut_EmptyContentTypeStoresOctetStream(t *testing.T) {
	svc := newService(t, byRoute(map[string]http.HandlerFunc{
		"PUT " + testKeyPath:  stored(`"abc"`, lastModified),
		"HEAD " + testKeyPath: object(`"abc"`, "application/octet-stream", "x"),
	}))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	obj, err := c.Put(t.Context(), "k", strings.NewReader("x"), storage.PutOptions{})
	if err != nil {
		t.Fatalf("Put = %v, want nil", err)
	}
	if obj.ContentType != "application/octet-stream" {
		t.Errorf("Put ContentType = %q, want application/octet-stream", obj.ContentType)
	}
	if got := svc.Requests()[0].Header.Get("Content-Type"); got != "application/octet-stream" {
		t.Errorf("PutObject Content-Type = %q, want application/octet-stream", got)
	}
}

// A HeadObject after the PutObject that reports another ETag, because a
// concurrent writer replaced the object, or that fails, leaves Put a
// success that reports its own ETag and the PutObject answer's Date.
func TestPut_ModifiedAtFallsBackToDate(t *testing.T) {
	date := lastModified.Add(time.Minute)
	for name, head := range map[string]http.HandlerFunc{
		"replaced": object(`"other"`, "text/plain", "x"),
		"failed":   status(http.StatusServiceUnavailable),
	} {
		t.Run(name, func(t *testing.T) {
			svc := newService(t, byRoute(map[string]http.HandlerFunc{
				"PUT " + testKeyPath:  stored(`"abc"`, date),
				"HEAD " + testKeyPath: head,
			}))
			c := newClient(t, testConfig(t, svc.endpoint(), nil))

			obj, err := c.Put(t.Context(), "k", strings.NewReader("x"), storage.PutOptions{})
			if err != nil {
				t.Fatalf("Put = %v, want nil: the object is written", err)
			}
			if obj.ETag != `"abc"` || !obj.ModifiedAt.Equal(date) {
				t.Errorf("Put ETag=%q ModifiedAt=%v, want %q and the Date %v", obj.ETag, obj.ModifiedAt, `"abc"`, date)
			}
		})
	}
}

// A body that disagrees with its declared size, or that fails, within the
// first part sends nothing, so nothing is stored.
func TestPut_BodyFailuresSendNothing(t *testing.T) {
	broke := errors.New("source broke")
	cases := []struct {
		name string
		body io.Reader
		size int64
		want func(error) bool
	}{
		{"shorter than declared", strings.NewReader("abc"), 4, func(err error) bool {
			return errors.Is(err, io.ErrUnexpectedEOF) && strings.Contains(err.Error(), "short of the declared size")
		}},
		{"longer than declared", strings.NewReader("abcde"), 4, func(err error) bool {
			return strings.Contains(err.Error(), "longer than the declared size")
		}},
		{"body fails", iotest.ErrReader(broke), 0, func(err error) bool { return errors.Is(err, broke) }},
		{"declared past a part, ends within one", bytes.NewReader(make([]byte, 8<<20)), 8<<20 + 1, func(err error) bool {
			return errors.Is(err, io.ErrUnexpectedEOF) && strings.Contains(err.Error(), "short of the declared size")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(t, status(http.StatusOK))
			c := newClient(t, testConfig(t, svc.endpoint(), nil))

			_, err := c.Put(t.Context(), "k", tc.body, storage.PutOptions{Size: tc.size})
			if err == nil || !tc.want(err) {
				t.Fatalf("Put = %v, want the body's failure", err)
			}
			if errors.Is(err, storage.ErrUnavailable) {
				t.Errorf("Put = %v, want a body failure unclassified", err)
			}
			if n := len(svc.Requests()); n != 0 {
				t.Errorf("service saw %d requests, want none", n)
			}
		})
	}
}

// A body of exactly one part, at the default 8 MiB, is one PutObject.
func TestPut_BodyOfOnePartIsOnePutObject(t *testing.T) {
	svc := newService(t, byRoute(map[string]http.HandlerFunc{
		"PUT " + testKeyPath:  stored(`"abc"`, lastModified),
		"HEAD " + testKeyPath: object(`"abc"`, "application/octet-stream", ""),
	}))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	obj, err := c.Put(t.Context(), "k", bytes.NewReader(make([]byte, 8<<20)), storage.PutOptions{})
	if err != nil {
		t.Fatalf("Put of 8 MiB = %v, want nil", err)
	}
	if obj.Size != 8<<20 {
		t.Errorf("Put Size = %d, want %d", obj.Size, 8<<20)
	}
	if ops := operations(svc.Requests()); !slices.Equal(ops, []string{"PutObject", "HeadObject"}) {
		t.Errorf("service saw %v, want one PutObject and a HeadObject", ops)
	}
}

// The part size the multipart tests configure, S3's 5 MiB minimum, and the
// upload ID the scripted service hands out.
const (
	testPartSize = 5 << 20
	testUploadID = "upload-1"
)

// operation names the S3 operation a recorded request is, by its method
// and query, as the multipart tests need to tell them apart.
func operation(r recorded) string {
	q, _ := url.ParseQuery(r.Query)
	switch {
	case r.Method == http.MethodPost && q.Has("uploads"):
		return "CreateMultipartUpload"
	case r.Method == http.MethodPut && q.Has("partNumber"):
		return "UploadPart"
	case r.Method == http.MethodPost && q.Has("uploadId"):
		return "CompleteMultipartUpload"
	case r.Method == http.MethodDelete && q.Has("uploadId"):
		return "AbortMultipartUpload"
	case r.Method == http.MethodPut:
		return "PutObject"
	case r.Method == http.MethodHead:
		return "HeadObject"
	}
	return r.Method + " " + r.Path
}

// operations names each recorded request, in order.
func operations(reqs []recorded) []string {
	ops := make([]string, len(reqs))
	for i, r := range reqs {
		ops[i] = operation(r)
	}
	return ops
}

// count returns how many of ops are op.
func count(ops []string, op string) int {
	n := 0
	for _, o := range ops {
		if o == op {
			n++
		}
	}
	return n
}

// multipart scripts a gateway's multipart upload of key k under
// testUploadID. A nil handler takes the success answer: CreateMultipartUpload
// hands out the upload ID, UploadPart stores a part, CompleteMultipartUpload
// answers with etag and a Date of lastModified plus a second, and
// AbortMultipartUpload answers 204. HeadObject reports the object under
// etag.
type multipart struct {
	etag     string
	part     http.HandlerFunc
	complete http.HandlerFunc
	abort    http.HandlerFunc
}

func (m multipart) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testKeyPath {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		h := map[string]http.HandlerFunc{
			"CreateMultipartUpload": func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><InitiateMultipartUploadResult><Bucket>%s</Bucket><Key>k</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`, testBucket, testUploadID)
			},
			"UploadPart": orElse(m.part, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("ETag", `"part-`+r.URL.Query().Get("partNumber")+`"`)
				w.WriteHeader(http.StatusOK)
			}),
			"CompleteMultipartUpload": orElse(m.complete, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				w.Header().Set("Date", lastModified.Add(time.Second).Format(http.TimeFormat))
				_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><CompleteMultipartUploadResult><Bucket>%s</Bucket><Key>k</Key><ETag>%s</ETag></CompleteMultipartUploadResult>`, testBucket, html.EscapeString(m.etag))
			}),
			"AbortMultipartUpload": orElse(m.abort, status(http.StatusNoContent)),
			"HeadObject":           object(m.etag, "application/octet-stream", ""),
		}[operation(recorded{Method: r.Method, Query: r.URL.RawQuery})]
		if h == nil {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	}
}

// orElse returns h, or def when h is nil.
func orElse(h, def http.HandlerFunc) http.HandlerFunc {
	if h != nil {
		return h
	}
	return def
}

// partLengths returns the decoded length of each UploadPart's content, in
// part order.
func partLengths(t *testing.T, reqs []recorded) []int64 {
	t.Helper()
	var lengths []int64
	for _, r := range reqs {
		if operation(r) != "UploadPart" {
			continue
		}
		v := cmp.Or(r.Header.Get("X-Amz-Decoded-Content-Length"), r.Header.Get("Content-Length"))
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			t.Fatalf("UploadPart length %q: %v", v, err)
		}
		q, _ := url.ParseQuery(r.Query)
		part, _ := strconv.Atoi(q.Get("partNumber"))
		lengths = slices.Grow(lengths, part)[:max(len(lengths), part)]
		lengths[part-1] = n
	}
	return lengths
}

// A body longer than one part, of unknown or declared size, is a multipart
// upload in parts of the configured size, and Put reports the completed
// object's ETag in its "<hex>-<parts>" form with the HeadObject's
// ModifiedAt.
func TestPut_LongerThanAPartIsAMultipartUpload(t *testing.T) {
	cases := []struct {
		name     string
		size     int64
		declared bool
		parts    []int64
	}{
		{"one byte past a part", testPartSize + 1, false, []int64{testPartSize, 1}},
		{"12 MiB unknown size", 12 << 20, false, []int64{testPartSize, testPartSize, 2 << 20}},
		{"12 MiB declared", 12 << 20, true, []int64{testPartSize, testPartSize, 2 << 20}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			etag := fmt.Sprintf(`"0123456789abcdef0123456789abcdef-%d"`, len(tc.parts))
			svc := newService(t, multipart{etag: etag}.handler())
			c := newClient(t, testConfig(t, svc.endpoint(), map[string]string{"part_size": strconv.Itoa(testPartSize)}))

			opts := storage.PutOptions{ContentType: "video/mp4"}
			if tc.declared {
				opts.Size = tc.size
			}
			obj, err := c.Put(t.Context(), "k", bytes.NewReader(make([]byte, tc.size)), opts)
			if err != nil {
				t.Fatalf("Put = %v, want nil", err)
			}
			want := storage.Object{Key: "k", Size: tc.size, ContentType: "video/mp4", ETag: etag, ModifiedAt: lastModified}
			if obj != want {
				t.Errorf("Put = %+v, want %+v", obj, want)
			}

			reqs := svc.Requests()
			ops := operations(reqs)
			if ops[0] != "CreateMultipartUpload" || ops[len(ops)-2] != "CompleteMultipartUpload" || ops[len(ops)-1] != "HeadObject" ||
				count(ops, "UploadPart") != len(tc.parts) || count(ops, "AbortMultipartUpload") != 0 {
				t.Fatalf("service saw %v, want CreateMultipartUpload, %d UploadParts, CompleteMultipartUpload, HeadObject", ops, len(tc.parts))
			}
			if got := reqs[0].Header.Get("Content-Type"); got != "video/mp4" {
				t.Errorf("CreateMultipartUpload Content-Type = %q, want video/mp4", got)
			}
			if got := partLengths(t, reqs); !slices.Equal(got, tc.parts) {
				t.Errorf("part lengths = %v, want %v", got, tc.parts)
			}
		})
	}
}

// A multipart upload that fails, through its body, a size mismatch, a part,
// or the completion, returns the failure and aborts the upload: once by
// transfermanager and once more by Put. Nothing is completed.
func TestPut_MultipartFailuresAbort(t *testing.T) {
	broke := errors.New("source broke")
	sevenMiB := func() io.Reader { return bytes.NewReader(make([]byte, 7<<20)) }
	cases := []struct {
		name     string
		body     io.Reader
		size     int64
		m        multipart
		complete bool
		want     func(error) bool
	}{
		{"body fails midway", io.MultiReader(sevenMiB(), iotest.ErrReader(broke)), 0, multipart{}, false,
			func(err error) bool { return errors.Is(err, broke) && !errors.Is(err, storage.ErrUnavailable) }},
		{"shorter than declared", sevenMiB(), 12 << 20, multipart{}, false, func(err error) bool {
			return errors.Is(err, io.ErrUnexpectedEOF) && strings.Contains(err.Error(), "short of the declared size")
		}},
		{"longer than declared", bytes.NewReader(make([]byte, 12<<20)), 11 << 20, multipart{}, false, func(err error) bool {
			return strings.Contains(err.Error(), "longer than the declared size")
		}},
		{"part fails", sevenMiB(), 0, multipart{part: failWith(http.StatusServiceUnavailable, "ServiceUnavailable")}, false,
			func(err error) bool { return errors.Is(err, storage.ErrUnavailable) }},
		{"completion fails", sevenMiB(), 0, multipart{complete: failWith(http.StatusInternalServerError, "InternalError")}, true,
			func(err error) bool { return errors.Is(err, storage.ErrUnavailable) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.m.etag = `"never"`
			svc := newService(t, tc.m.handler())
			c := newClient(t, testConfig(t, svc.endpoint(), map[string]string{"part_size": strconv.Itoa(testPartSize)}))

			_, err := c.Put(t.Context(), "k", tc.body, storage.PutOptions{Size: tc.size})
			if err == nil || !tc.want(err) {
				t.Fatalf("Put = %v, want the upload's failure", err)
			}
			if strings.Contains(err.Error(), "parts may remain") {
				t.Errorf("Put = %v, want no abort failure", err)
			}
			wantAborts(t, svc.Requests(), 2, tc.complete)
		})
	}
}

// wantAborts asserts the service saw aborts AbortMultipartUploads, each for
// testUploadID, after the upload, and a CompleteMultipartUpload only when
// completed.
func wantAborts(t *testing.T, reqs []recorded, aborts int, completed bool) {
	t.Helper()
	ops := operations(reqs)
	if count(ops, "AbortMultipartUpload") != aborts || (count(ops, "CompleteMultipartUpload") == 1) != completed ||
		count(ops, "HeadObject") != 0 {
		t.Fatalf("service saw %v, want %d AbortMultipartUploads, a completion %v, and no HeadObject", ops, aborts, completed)
	}
	for _, r := range reqs {
		if operation(r) != "AbortMultipartUpload" {
			continue
		}
		if q, _ := url.ParseQuery(r.Query); q.Get("uploadId") != testUploadID {
			t.Errorf("AbortMultipartUpload for upload %q, want %q", q.Get("uploadId"), testUploadID)
		}
	}
}

// cancelAfter reads r and cancels the context once n bytes have been read,
// as a caller that gives up midway through a Put does.
type cancelAfter struct {
	r      io.Reader
	n      int64
	cancel context.CancelFunc
}

func (c *cancelAfter) Read(p []byte) (int, error) {
	k, err := c.r.Read(p)
	if c.n -= int64(k); c.n <= 0 {
		c.cancel()
	}
	return k, err
}

// A caller that cancels midway through a multipart upload gets its
// cancellation back, unclassified, and the upload is still aborted on a
// context of its own.
func TestPut_CancelledMultipartStillAborts(t *testing.T) {
	svc := newService(t, multipart{etag: `"never"`}.handler())
	c := newClient(t, testConfig(t, svc.endpoint(), map[string]string{"part_size": strconv.Itoa(testPartSize)}))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	body := &cancelAfter{r: bytes.NewReader(make([]byte, 12<<20)), n: 7 << 20, cancel: cancel}
	_, err := c.Put(ctx, "k", body, storage.PutOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Put = %v, want context.Canceled", err)
	}
	if errors.Is(err, storage.ErrUnavailable) {
		t.Errorf("Put = %v, want the cancellation unclassified", err)
	}
	wantAborts(t, svc.Requests(), 2, false)
}

// transfermanager drops the error of an abort that fails after a part
// failed, so Put's own abort is what frees the upload. When that one fails
// too, Put names it in the error, which keeps the part failure's
// classification.
func TestPut_FailedAbortIsRepeated(t *testing.T) {
	for name, tc := range map[string]struct {
		abortStatus []int
		remain      bool
	}{
		"repeat succeeds": {[]int{http.StatusInternalServerError, http.StatusNoContent}, false},
		"repeat fails":    {[]int{http.StatusInternalServerError, http.StatusInternalServerError}, true},
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			aborts := 0
			m := multipart{
				etag: `"never"`,
				part: failWith(http.StatusForbidden, "AccessDenied"),
				abort: func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					code := tc.abortStatus[aborts]
					aborts++
					mu.Unlock()
					if code == http.StatusNoContent {
						w.WriteHeader(code)
						return
					}
					s3Error(w, r, code, "InternalError")
				},
			}
			svc := newService(t, m.handler())
			c := newClient(t, testConfig(t, svc.endpoint(), map[string]string{"part_size": strconv.Itoa(testPartSize)}))

			_, err := c.Put(t.Context(), "k", bytes.NewReader(make([]byte, 7<<20)), storage.PutOptions{})
			if errorCode(err) != "AccessDenied" || errors.Is(err, storage.ErrUnavailable) {
				t.Fatalf("Put = %v, want the part's AccessDenied, unclassified", err)
			}
			if got := strings.Contains(err.Error(), "parts may remain"); got != tc.remain {
				t.Errorf("Put = %v, want an abort failure named: %v", err, tc.remain)
			}
			wantAborts(t, svc.Requests(), 2, false)
		})
	}
}

// errorCode returns the S3 error code err carries, or "".
func errorCode(err error) string {
	if apiErr, ok := errors.AsType[smithy.APIError](err); ok {
		return apiErr.ErrorCode()
	}
	return ""
}

// Every object operation checks its key against Capabilities before it
// sends anything.
func TestObjectOperations_RejectInvalidKeys(t *testing.T) {
	for name, key := range map[string]string{
		"empty":         "",
		"too long":      strings.Repeat("k", 1025),
		"invalid utf-8": "a\xffb",
	} {
		t.Run(name, func(t *testing.T) {
			svc := newService(t, status(http.StatusOK))
			c := newClient(t, testConfig(t, svc.endpoint(), nil))
			ctx := t.Context()

			_, putErr := c.Put(ctx, key, strings.NewReader("x"), storage.PutOptions{})
			_, getErr := c.Get(ctx, key, storage.GetOptions{})
			_, statErr := c.Stat(ctx, key)
			deleteErr := c.Delete(ctx, key)
			for op, err := range map[string]error{"Put": putErr, "Get": getErr, "Stat": statErr, "Delete": deleteErr} {
				if err == nil || !strings.HasPrefix(err.Error(), "s3: ") || err.Error() != c.Capabilities().ValidateKey(key).Error() {
					t.Errorf("%s(%q) = %v, want ValidateKey's error", op, key, err)
				}
			}
			if n := len(svc.Requests()); n != 0 {
				t.Errorf("service saw %d requests, want none", n)
			}
		})
	}
}

func TestGet_StreamsTheObjectWithItsMetadata(t *testing.T) {
	svc := newService(t, byRoute(map[string]http.HandlerFunc{
		"GET " + testKeyPath: object(`"abc"`, "text/plain", "hello"),
	}))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	blob, err := c.Get(t.Context(), "k", storage.GetOptions{})
	if err != nil {
		t.Fatalf("Get = %v, want nil", err)
	}
	data, err := io.ReadAll(blob.Body)
	_ = blob.Body.Close()
	if err != nil || string(data) != "hello" {
		t.Fatalf("Get body = %q, %v, want %q", data, err, "hello")
	}
	want := storage.Object{Key: "k", Size: 5, ContentType: "text/plain", ETag: `"abc"`, ModifiedAt: lastModified}
	if blob.Object != want {
		t.Errorf("Get = %+v, want %+v", blob.Object, want)
	}
}

func TestStat_ReportsMetadata(t *testing.T) {
	svc := newService(t, byRoute(map[string]http.HandlerFunc{
		"HEAD " + testKeyPath: object(`"abc"`, "application/json", "{}"),
	}))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	obj, err := c.Stat(t.Context(), "k")
	if err != nil {
		t.Fatalf("Stat = %v, want nil", err)
	}
	want := storage.Object{Key: "k", Size: 2, ContentType: "application/json", ETag: `"abc"`, ModifiedAt: lastModified}
	if obj != want {
		t.Errorf("Stat = %+v, want %+v", obj, want)
	}
}

// Every operation reports the ETag in HTTP entity-tag form, whether the
// service sent it quoted, unquoted, or weak.
func TestETag_EntityTagForm(t *testing.T) {
	for sent, want := range map[string]string{
		`"abc"`:   `"abc"`,
		`abc`:     `"abc"`,
		`W/"abc"`: `W/"abc"`,
		`"a-3"`:   `"a-3"`,
	} {
		t.Run(sent, func(t *testing.T) {
			svc := newService(t, byRoute(map[string]http.HandlerFunc{
				"PUT " + testKeyPath:  stored(sent, lastModified),
				"GET " + testKeyPath:  object(sent, "text/plain", "x"),
				"HEAD " + testKeyPath: object(sent, "text/plain", "x"),
				"GET /" + testBucket: listing(fmt.Sprintf(`<KeyCount>1</KeyCount><IsTruncated>false</IsTruncated><Contents><Key>k</Key><ETag>%s</ETag><Size>1</Size><LastModified>2026-10-07T12:30:45.000Z</LastModified></Contents>`,
					strings.ReplaceAll(sent, `"`, "&quot;"))),
			}))
			c := newClient(t, testConfig(t, svc.endpoint(), nil))
			ctx := t.Context()

			put, err := c.Put(ctx, "k", strings.NewReader("x"), storage.PutOptions{})
			if err != nil {
				t.Fatalf("Put: %v", err)
			}
			blob, err := c.Get(ctx, "k", storage.GetOptions{})
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			_ = blob.Body.Close()
			stat, err := c.Stat(ctx, "k")
			if err != nil {
				t.Fatalf("Stat: %v", err)
			}
			page, err := c.List(ctx, storage.ListOptions{})
			if err != nil || len(page.Objects) != 1 {
				t.Fatalf("List = %+v, %v, want one object", page, err)
			}
			for op, got := range map[string]string{"Put": put.ETag, "Get": blob.ETag, "Stat": stat.ETag, "List": page.Objects[0].ETag} {
				if got != want {
					t.Errorf("%s ETag = %q, want %q", op, got, want)
				}
			}
			if !page.Objects[0].ModifiedAt.Equal(stat.ModifiedAt) {
				t.Errorf("List ModifiedAt = %v, want Stat's %v", page.Objects[0].ModifiedAt, stat.ModifiedAt)
			}
		})
	}
}

// HeadObject answers a missing key and a missing bucket with the same bare
// 404; the HeadBucket that follows decides which one Stat reports.
func TestStat_HeadBucketDisambiguatesA404(t *testing.T) {
	cases := []struct {
		name   string
		bucket http.HandlerFunc
		want   error
	}{
		{"bucket exists", status(http.StatusOK), storage.ErrNotFound},
		{"bucket missing", status(http.StatusNotFound), storage.ErrContainerNotFound},
		{"bucket check fails", status(http.StatusServiceUnavailable), storage.ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newService(t, byRoute(map[string]http.HandlerFunc{
				"HEAD " + testKeyPath: status(http.StatusNotFound),
				"HEAD /" + testBucket: tc.bucket,
			}))
			c := newClient(t, testConfig(t, svc.endpoint(), nil))

			_, err := c.Stat(t.Context(), "k")
			wantOnly(t, "Stat", err, tc.want)
			reqs := svc.Requests()
			if len(reqs) != 2 || reqs[0].Path != testKeyPath || reqs[1].Path != "/"+testBucket {
				t.Fatalf("service saw %+v, want HEAD %s then HEAD /%s", reqs, testKeyPath, testBucket)
			}
		})
	}
}

// A HeadObject failure other than a bare 404 is classified on its own,
// with no HeadBucket after it.
func TestStat_OtherFailuresSkipTheBucketCheck(t *testing.T) {
	svc := newService(t, status(http.StatusForbidden))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	_, err := c.Stat(t.Context(), "k")
	if err == nil || errors.Is(err, storage.ErrNotFound) || errors.Is(err, storage.ErrContainerNotFound) {
		t.Fatalf("Stat on 403 = %v, want it unclassified", err)
	}
	if n := len(svc.Requests()); n != 1 {
		t.Errorf("service saw %d requests, want the one HEAD", n)
	}
}

// Each operation whose answer carries a body classifies the S3 error code
// it names.
func TestObjectOperations_ClassifyErrors(t *testing.T) {
	ops := map[string]func(ctx context.Context, c storage.Client) error{
		"Put": func(ctx context.Context, c storage.Client) error {
			_, err := c.Put(ctx, "k", strings.NewReader("x"), storage.PutOptions{})
			return err
		},
		"Get": func(ctx context.Context, c storage.Client) error {
			_, err := c.Get(ctx, "k", storage.GetOptions{})
			return err
		},
		"Delete": func(ctx context.Context, c storage.Client) error { return c.Delete(ctx, "k") },
		"List": func(ctx context.Context, c storage.Client) error {
			_, err := c.List(ctx, storage.ListOptions{})
			return err
		},
	}
	answers := []struct {
		status int
		code   string
		want   error
	}{
		{http.StatusNotFound, "NoSuchBucket", storage.ErrContainerNotFound},
		{http.StatusInternalServerError, "InternalError", storage.ErrUnavailable},
		{http.StatusServiceUnavailable, "SlowDown", storage.ErrUnavailable},
	}
	for op, call := range ops {
		for _, a := range answers {
			t.Run(op+"/"+a.code, func(t *testing.T) {
				svc := newService(t, failWith(a.status, a.code))
				wantOnly(t, op, call(t.Context(), newClient(t, testConfig(t, svc.endpoint(), nil))), a.want)
			})
		}
		t.Run(op+"/AccessDenied", func(t *testing.T) {
			svc := newService(t, failWith(http.StatusForbidden, "AccessDenied"))
			err := call(t.Context(), newClient(t, testConfig(t, svc.endpoint(), nil)))
			if err == nil {
				t.Fatalf("%s on AccessDenied = nil, want an error", op)
			}
			for _, s := range []error{storage.ErrNotFound, storage.ErrContainerNotFound, storage.ErrUnavailable} {
				if errors.Is(err, s) {
					t.Fatalf("%s on AccessDenied = %v, want it unclassified", op, err)
				}
			}
		})
	}
}

func TestGet_MissingKey(t *testing.T) {
	svc := newService(t, failWith(http.StatusNotFound, "NoSuchKey"))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	_, err := c.Get(t.Context(), "k", storage.GetOptions{})
	wantOnly(t, "Get", err, storage.ErrNotFound)
}

// A Get body whose connection drops partway fails its read with
// storage.ErrUnavailable, as a request that gets no answer does, with the
// transport's error still matchable.
func TestGet_BodyReadFailureIsUnavailable(t *testing.T) {
	svc := newService(t, byRoute(map[string]http.HandlerFunc{
		"GET " + testKeyPath: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("ETag", `"abc"`)
			w.Header().Set("Content-Length", "10")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "hello")
			conn, _, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
		},
	}))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	blob, err := c.Get(t.Context(), "k", storage.GetOptions{})
	if err != nil {
		t.Fatalf("Get = %v, want nil: the answer's headers arrived", err)
	}
	defer func() { _ = blob.Body.Close() }()
	data, err := io.ReadAll(blob.Body)
	if !errors.Is(err, storage.ErrUnavailable) || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read of a body cut off after %q = %v, want ErrUnavailable wrapping io.ErrUnexpectedEOF", data, err)
	}
}

// S3 answers a delete of a missing key with 204; a gateway's NoSuchKey is
// success too.
func TestDelete_MissingKeySucceeds(t *testing.T) {
	for name, answer := range map[string]http.HandlerFunc{
		"204":       status(http.StatusNoContent),
		"NoSuchKey": failWith(http.StatusNotFound, "NoSuchKey"),
	} {
		t.Run(name, func(t *testing.T) {
			svc := newService(t, answer)
			c := newClient(t, testConfig(t, svc.endpoint(), nil))

			if err := c.Delete(t.Context(), "k"); err != nil {
				t.Fatalf("Delete of a missing key = %v, want nil", err)
			}
			reqs := svc.Requests()
			if len(reqs) != 1 || reqs[0].Method != http.MethodDelete || reqs[0].Path != testKeyPath {
				t.Fatalf("service saw %+v, want one DELETE %s", reqs, testKeyPath)
			}
		})
	}
}

// List sends the prefix, token, and limit as ListObjectsV2's parameters and
// returns the continuation token as Next only while the listing is
// truncated.
func TestList_PagesWithTheContinuationToken(t *testing.T) {
	svc := newService(t, listing(`<Prefix>p/</Prefix><KeyCount>2</KeyCount><MaxKeys>2</MaxKeys><IsTruncated>true</IsTruncated><NextContinuationToken>tok-2</NextContinuationToken>`+
		`<Contents><Key>p/a</Key><ETag>&quot;e1&quot;</ETag><Size>3</Size><LastModified>2026-10-07T12:30:45.250Z</LastModified></Contents>`+
		`<Contents><Key>p/b</Key><ETag>&quot;e2&quot;</ETag><Size>4</Size><LastModified>2026-10-07T12:30:46Z</LastModified></Contents>`))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	page, err := c.List(t.Context(), storage.ListOptions{Prefix: "p/", Token: "tok-1", Limit: 2})
	if err != nil {
		t.Fatalf("List = %v, want nil", err)
	}
	if page.Next != "tok-2" {
		t.Errorf("Next = %q, want tok-2", page.Next)
	}
	want := []storage.Object{
		{Key: "p/a", Size: 3, ETag: `"e1"`, ModifiedAt: lastModified},
		{Key: "p/b", Size: 4, ETag: `"e2"`, ModifiedAt: lastModified.Add(time.Second)},
	}
	if len(page.Objects) != len(want) {
		t.Fatalf("List = %+v, want %+v", page.Objects, want)
	}
	for i := range want {
		if page.Objects[i] != want[i] {
			t.Errorf("object %d = %+v, want %+v", i, page.Objects[i], want[i])
		}
	}

	q := svc.Requests()[0].Query
	for _, p := range []string{"list-type=2", "prefix=p%2F", "continuation-token=tok-1", "max-keys=2"} {
		if !strings.Contains(q, p) {
			t.Errorf("ListObjectsV2 query = %q, want it to carry %s", q, p)
		}
	}
}

func TestList_LastPageHasNoNext(t *testing.T) {
	// A gateway may send a token on the last page; IsTruncated decides.
	svc := newService(t, listing(`<KeyCount>0</KeyCount><IsTruncated>false</IsTruncated><NextContinuationToken>stale</NextContinuationToken>`))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	page, err := c.List(t.Context(), storage.ListOptions{})
	if err != nil {
		t.Fatalf("List = %v, want nil", err)
	}
	if page.Next != "" || len(page.Objects) != 0 {
		t.Errorf("List = %+v, want no objects and an empty Next", page)
	}
	q := svc.Requests()[0].Query
	for _, p := range []string{"prefix=", "continuation-token=", "max-keys="} {
		if strings.Contains(q, p) {
			t.Errorf("ListObjectsV2 query = %q, want no %s for an unset option", q, p)
		}
	}
}

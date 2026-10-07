package s3_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// A body that disagrees with its declared size, that fails, or that runs
// past the single-part limit sends nothing, so nothing is stored.
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
		{"declared past the single-part limit", strings.NewReader("x"), 8<<20 + 1, func(err error) bool {
			return errors.Is(err, errors.ErrUnsupported)
		}},
		{"body past the single-part limit", bytes.NewReader(make([]byte, 8<<20+1)), 0, func(err error) bool {
			return errors.Is(err, errors.ErrUnsupported)
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

// The single-part limit admits a body of exactly its size.
func TestPut_BodyAtTheSinglePartLimit(t *testing.T) {
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

package s3_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"

	"github.com/JaimeStill/spike-s3-storage/s3"
	"github.com/standards-lab/go-storage"
)

func newClient(t *testing.T, cfg storage.Config) *s3.Client {
	t.Helper()
	c, err := s3.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestNew_RequiresContainerAccountAndKey(t *testing.T) {
	cases := []struct {
		name  string
		strip func(*storage.Config)
		want  string
	}{
		{"container", func(c *storage.Config) { c.Container = "" }, "container (bucket) required"},
		{"account", func(c *storage.Config) { c.Account = "" }, "account (access key ID) required"},
		{"key", func(c *storage.Config) { c.Key = "" }, "key (secret access key) required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Finalize first, since Finalize itself rejects an empty
			// container; the field is cleared afterwards so New's own check
			// is what fails.
			cfg := testConfig(t, "http://127.0.0.1:8333", nil)
			tc.strip(&cfg)
			_, err := s3.New(cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestNew_RejectsInvalidBucketNames(t *testing.T) {
	for _, name := range []string{
		"ab",
		strings.Repeat("a", 64),
		"Upper",
		"under_score",
		"-leading",
		"trailing-",
		".leading",
		"trailing.",
		"two..dots",
		"192.168.5.4",
		"xn--bucket",
		"sthree-bucket",
		"amzn-s3-demo-bucket",
		"bucket-s3alias",
		"bucket--ol-s3",
		"bucket.mrap",
		"bucket--x-s3",
		"bucket--table-s3",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t, "http://127.0.0.1:8333", nil)
			cfg.Container = name
			_, err := s3.New(cfg)
			if err == nil || !strings.Contains(err.Error(), "bucket name") {
				t.Fatalf("New with bucket %q = %v, want a bucket-name error", name, err)
			}
		})
	}
}

func TestNew_AcceptsValidBucketNames(t *testing.T) {
	for _, name := range []string{
		"abc",
		strings.Repeat("a", 63),
		"my-bucket.v2",
		"0-9",
		"999.168.5.4",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t, "http://127.0.0.1:8333", nil)
			cfg.Container = name
			if _, err := s3.New(cfg); err != nil {
				t.Fatalf("New with bucket %q = %v, want nil", name, err)
			}
		})
	}
}

func TestNew_RejectsBadEndpoint(t *testing.T) {
	for _, endpoint := range []string{"127.0.0.1:8333", "ftp://host", "http://", "http://[::1"} {
		t.Run(endpoint, func(t *testing.T) {
			_, err := s3.New(testConfig(t, endpoint, nil))
			if err == nil || !strings.Contains(err.Error(), "endpoint") {
				t.Fatalf("New with endpoint %q = %v, want an endpoint error", endpoint, err)
			}
		})
	}
}

func TestNew_RejectsBadMaxRetries(t *testing.T) {
	for _, v := range []string{"-1", "x", "1.5", ""} {
		t.Run(v, func(t *testing.T) {
			_, err := s3.New(testConfig(t, "http://127.0.0.1:8333", map[string]string{"max_retries": v}))
			if err == nil || !strings.Contains(err.Error(), "max_retries") {
				t.Fatalf("New = %v, want an error naming max_retries", err)
			}
		})
	}
}

// part_size takes a whole byte count within S3's part bounds, 5 MiB to
// 5 GiB.
func TestNew_RejectsBadPartSize(t *testing.T) {
	for _, v := range []string{"x", "", "-1", "5242879", "5368709121", "8MiB"} {
		t.Run(v, func(t *testing.T) {
			_, err := s3.New(testConfig(t, "http://127.0.0.1:8333", map[string]string{"part_size": v}))
			if err == nil || !strings.Contains(err.Error(), "part_size") {
				t.Fatalf("New = %v, want an error naming part_size", err)
			}
		})
	}
}

func TestNew_AcceptsPartSizeBounds(t *testing.T) {
	for _, v := range []string{"5242880", "5368709120"} {
		if _, err := s3.New(testConfig(t, "http://127.0.0.1:8333", map[string]string{"part_size": v})); err != nil {
			t.Errorf("New with part_size %s = %v, want nil", v, err)
		}
	}
}

func TestNew_RejectsEmptyRegion(t *testing.T) {
	_, err := s3.New(testConfig(t, "http://127.0.0.1:8333", map[string]string{"region": ""}))
	if err == nil || !strings.Contains(err.Error(), "region") {
		t.Fatalf("New = %v, want an error naming region", err)
	}
}

func TestNew_PanicsOnUnfinalizedConfig(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("New with an unfinalized config did not panic")
		}
		if msg, _ := r.(string); !strings.Contains(msg, "Finalize") {
			t.Fatalf("panic = %v, want the fix named", r)
		}
	}()
	_, _ = s3.New(storage.Config{Container: testBucket, Account: testAccount, Key: testKey})
}

// New performs no I/O: a client aimed at a port nothing listens on builds.
func TestNew_PerformsNoIO(t *testing.T) {
	newClient(t, testConfig(t, closedEndpoint(t), nil))
}

// A set Endpoint is the base endpoint, addressed path-style, and requests
// are signed with the access key for the default region.
func TestProbe_PathStyleSignedForDefaultRegion(t *testing.T) {
	svc := newService(t, status(http.StatusOK))
	c := newClient(t, testConfig(t, svc.endpoint(), map[string]string{"ignored": "x"}))

	if err := c.Probe(t.Context()); err != nil {
		t.Fatalf("Probe = %v, want nil", err)
	}
	reqs := svc.Requests()
	if len(reqs) != 1 {
		t.Fatalf("service saw %d requests, want 1", len(reqs))
	}
	r := reqs[0]
	if r.Method != http.MethodHead || r.Path != "/"+testBucket {
		t.Errorf("request = %s %s, want HEAD /%s", r.Method, r.Path, testBucket)
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential="+testAccount+"/") ||
		!strings.Contains(auth, "/us-east-1/s3/aws4_request") {
		t.Errorf("Authorization = %q, want a SigV4 signature by %s for us-east-1", auth, testAccount)
	}
}

func TestProbe_RegionOption(t *testing.T) {
	svc := newService(t, status(http.StatusOK))
	c := newClient(t, testConfig(t, svc.endpoint(), map[string]string{"region": "eu-west-1"}))

	if err := c.Probe(t.Context()); err != nil {
		t.Fatalf("Probe = %v, want nil", err)
	}
	if auth := svc.Requests()[0].Header.Get("Authorization"); !strings.Contains(auth, "/eu-west-1/s3/aws4_request") {
		t.Errorf("Authorization = %q, want a signature scoped to eu-west-1", auth)
	}
}

func TestProbe_BucketNotFound(t *testing.T) {
	svc := newService(t, failWith(http.StatusNotFound, "NoSuchBucket"))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	err := c.Probe(t.Context())
	if !errors.Is(err, storage.ErrContainerNotFound) || errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Probe = %v, want ErrContainerNotFound and not ErrNotFound", err)
	}
	if _, ok := errors.AsType[smithy.APIError](err); !ok {
		t.Fatalf("Probe = %v, want the SDK's APIError matchable", err)
	}
}

func TestProbe_ServerError(t *testing.T) {
	for _, code := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			svc := newService(t, status(code))
			c := newClient(t, testConfig(t, svc.endpoint(), nil))

			err := c.Probe(t.Context())
			if !errors.Is(err, storage.ErrUnavailable) {
				t.Fatalf("Probe = %v, want ErrUnavailable", err)
			}
			respErr, ok := errors.AsType[*smithyhttp.ResponseError](err)
			if !ok || respErr.HTTPStatusCode() != code {
				t.Fatalf("Probe = %v, want the SDK's ResponseError with status %d", err, code)
			}
			// max_retries=0 means one try: the scripted 5xx is retryable to
			// the SDK, and the option is what stopped it.
			if n := len(svc.Requests()); n != 1 {
				t.Errorf("service saw %d requests, want 1 with max_retries=0", n)
			}
		})
	}
}

func TestProbe_Forbidden(t *testing.T) {
	svc := newService(t, status(http.StatusForbidden))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	err := c.Probe(t.Context())
	if err == nil {
		t.Fatal("Probe on 403 = nil, want an error")
	}
	if errors.Is(err, storage.ErrUnavailable) || errors.Is(err, storage.ErrContainerNotFound) {
		t.Fatalf("Probe on 403 = %v, want it unclassified", err)
	}
}

func TestProbe_ConnectionRefused(t *testing.T) {
	c := newClient(t, testConfig(t, closedEndpoint(t), nil))

	err := c.Probe(t.Context())
	if !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("Probe against a closed port = %v, want ErrUnavailable", err)
	}
	// The SDK wraps a send failure in a ResponseError with no response.
	if respErr, ok := errors.AsType[*smithyhttp.ResponseError](err); ok && respErr.HTTPStatusCode() != 0 {
		t.Fatalf("Probe against a closed port = %v, want no HTTP status in the chain", err)
	}
}

func TestProbe_CallerCancelled(t *testing.T) {
	arrived := make(chan struct{})
	svc := newService(t, func(w http.ResponseWriter, r *http.Request) {
		close(arrived)
		<-r.Context().Done()
	})
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-arrived
		cancel()
	}()
	err := c.Probe(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Probe under a cancelled context = %v, want context.Canceled", err)
	}
	if errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("Probe under a cancelled context = %v, want it unclassified", err)
	}
}

func TestProbe_CallerDeadline(t *testing.T) {
	svc := newService(t, func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := c.Probe(ctx)
	if !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("Probe past its deadline = %v, want ErrUnavailable", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Probe past its deadline = %v, want context.DeadlineExceeded still matchable", err)
	}
}

func TestMaxRetries_BoundsTheSDKRetryCount(t *testing.T) {
	svc := newService(t, failWith(http.StatusServiceUnavailable, "SlowDown"))
	c := newClient(t, testConfig(t, svc.endpoint(), map[string]string{"max_retries": "1"}))

	if err := c.Probe(t.Context()); !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("Probe = %v, want ErrUnavailable", err)
	}
	if n := len(svc.Requests()); n != 2 {
		t.Errorf("service saw %d requests, want 2 with max_retries=1", n)
	}
}

func TestEnsureContainer_Created(t *testing.T) {
	svc := newService(t, status(http.StatusOK))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	if err := c.EnsureContainer(t.Context()); err != nil {
		t.Fatalf("EnsureContainer = %v, want nil", err)
	}
	reqs := svc.Requests()
	if len(reqs) != 1 || reqs[0].Method != http.MethodPut || reqs[0].Path != "/"+testBucket {
		t.Fatalf("service saw %+v, want one PUT /%s", reqs, testBucket)
	}
	// us-east-1 takes no location constraint.
	if len(reqs[0].Body) != 0 {
		t.Errorf("CreateBucket body = %q, want none in us-east-1", reqs[0].Body)
	}
}

func TestEnsureContainer_LocationConstraintOutsideUSEast1(t *testing.T) {
	svc := newService(t, status(http.StatusOK))
	c := newClient(t, testConfig(t, svc.endpoint(), map[string]string{"region": "eu-west-1"}))

	if err := c.EnsureContainer(t.Context()); err != nil {
		t.Fatalf("EnsureContainer = %v, want nil", err)
	}
	if body := string(svc.Requests()[0].Body); !strings.Contains(body, "<LocationConstraint>eu-west-1</LocationConstraint>") {
		t.Errorf("CreateBucket body = %q, want the eu-west-1 location constraint", body)
	}
}

func TestEnsureContainer_AlreadyOwnedByYou(t *testing.T) {
	svc := newService(t, failWith(http.StatusConflict, "BucketAlreadyOwnedByYou"))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	if err := c.EnsureContainer(t.Context()); err != nil {
		t.Fatalf("EnsureContainer over an owned bucket = %v, want nil", err)
	}
	if reqs := svc.Requests(); len(reqs) != 1 || reqs[0].Method != http.MethodPut {
		t.Fatalf("service saw %+v, want the one PUT and nothing more", reqs)
	}
}

// BucketAlreadyExists is success only when the bucket then answers a probe.
func TestEnsureContainer_AlreadyExistsAndReachable(t *testing.T) {
	svc := newService(t, byMethod(map[string]http.HandlerFunc{
		http.MethodPut:  failWith(http.StatusConflict, "BucketAlreadyExists"),
		http.MethodHead: status(http.StatusOK),
	}))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	if err := c.EnsureContainer(t.Context()); err != nil {
		t.Fatalf("EnsureContainer = %v, want nil", err)
	}
	reqs := svc.Requests()
	if len(reqs) != 2 || reqs[1].Method != http.MethodHead {
		t.Fatalf("service saw %+v, want a PUT then a HEAD", reqs)
	}
}

func TestEnsureContainer_AlreadyExistsElsewhere(t *testing.T) {
	svc := newService(t, byMethod(map[string]http.HandlerFunc{
		http.MethodPut:  failWith(http.StatusConflict, "BucketAlreadyExists"),
		http.MethodHead: status(http.StatusForbidden),
	}))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	err := c.EnsureContainer(t.Context())
	if err == nil {
		t.Fatal("EnsureContainer over another account's bucket = nil, want an error")
	}
	apiErr, ok := errors.AsType[smithy.APIError](err)
	if !ok || apiErr.ErrorCode() != "BucketAlreadyExists" {
		t.Fatalf("EnsureContainer = %v, want the BucketAlreadyExists APIError matchable", err)
	}
	if errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("EnsureContainer = %v, want it unclassified", err)
	}
}

func TestEnsureContainer_ServerError(t *testing.T) {
	svc := newService(t, failWith(http.StatusInternalServerError, "InternalError"))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	err := c.EnsureContainer(t.Context())
	if !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("EnsureContainer = %v, want ErrUnavailable", err)
	}
	apiErr, ok := errors.AsType[smithy.APIError](err)
	if !ok || apiErr.ErrorCode() != "InternalError" {
		t.Fatalf("EnsureContainer = %v, want the InternalError APIError matchable", err)
	}
}

func TestEnsureContainer_Forbidden(t *testing.T) {
	svc := newService(t, failWith(http.StatusForbidden, "AccessDenied"))
	c := newClient(t, testConfig(t, svc.endpoint(), nil))

	err := c.EnsureContainer(t.Context())
	if err == nil {
		t.Fatal("EnsureContainer on 403 = nil, want an error")
	}
	if errors.Is(err, storage.ErrUnavailable) || errors.Is(err, storage.ErrContainerNotFound) {
		t.Fatalf("EnsureContainer on 403 = %v, want it unclassified", err)
	}
	respErr, ok := errors.AsType[*smithyhttp.ResponseError](err)
	if !ok || respErr.HTTPStatusCode() != http.StatusForbidden {
		t.Fatalf("EnsureContainer = %v, want the SDK's ResponseError with status 403", err)
	}
}

func TestEnsureContainer_ConnectionRefused(t *testing.T) {
	c := newClient(t, testConfig(t, closedEndpoint(t), nil))

	if err := c.EnsureContainer(t.Context()); !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("EnsureContainer against a closed port = %v, want ErrUnavailable", err)
	}
}

// MaxKeyLength declares S3's 1,024-byte limit, and ValidateKey holds keys to
// exactly that bound. The other rules ValidateKey applies are proved in
// keys_test.go.
func TestCapabilities_MaxKeyLengthIsValidateKeysBound(t *testing.T) {
	caps := newClient(t, testConfig(t, "http://127.0.0.1:8333", nil)).Capabilities()

	if caps.MaxKeyLength != 1024 {
		t.Errorf("MaxKeyLength = %d, want 1024", caps.MaxKeyLength)
	}
	if err := caps.ValidateKey(strings.Repeat("k", caps.MaxKeyLength)); err != nil {
		t.Errorf("ValidateKey of MaxKeyLength bytes = %v, want nil", err)
	}
	if err := caps.ValidateKey(strings.Repeat("k", caps.MaxKeyLength+1)); err == nil {
		t.Errorf("ValidateKey of MaxKeyLength+1 bytes = nil, want an error")
	}
}

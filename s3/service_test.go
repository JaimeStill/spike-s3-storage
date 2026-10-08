package s3_test

import (
	"bytes"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/standards-lab/go-storage"
)

// The credential the unit tests sign with and the bucket they address. The
// acceptance tests sign with the same pair, which the SeaweedFS harness
// configures as its admin identity.
const (
	testAccount = "admin"
	testKey     = "secret"
	testBucket  = "unit"
)

// service is a scripted stand-in for an S3 gateway: an httptest server whose
// handler answers every request through respond and records what it
// received. The client addresses it path-style, so the bucket is the first
// path segment.
type service struct {
	srv     *httptest.Server
	respond http.HandlerFunc

	mu       sync.Mutex
	requests []recorded
}

// recorded is what a test can assert about one request the service saw.
type recorded struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
}

// newService starts a service that answers with respond and stops it when
// the test ends.
func newService(t *testing.T, respond http.HandlerFunc) *service {
	t.Helper()
	s := &service{respond: respond}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *service) handle(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	s.mu.Lock()
	s.requests = append(s.requests, recorded{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.RawQuery,
		Header: r.Header.Clone(),
		Body:   body,
	})
	s.mu.Unlock()
	s.respond(w, r)
}

// endpoint is the service URL a Config's Endpoint takes.
func (s *service) endpoint() string {
	return s.srv.URL
}

// Requests returns a copy of every request the service has recorded.
func (s *service) Requests() []recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recorded(nil), s.requests...)
}

// s3Error writes the failure shape S3 uses: the status and an XML Error body
// naming the code. A HEAD answer drops the body, as S3's does.
func s3Error(w http.ResponseWriter, r *http.Request, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>scripted by the test</Message><RequestId>unit</RequestId></Error>`, code)
}

// status answers every request with one status and no body.
func status(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	}
}

// failWith answers every request with one S3 error.
func failWith(status int, code string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s3Error(w, r, status, code)
	}
}

// byMethod answers each request with the handler for its method, and with
// 405 for a method it has none for.
func byMethod(handlers map[string]http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h, ok := handlers[r.Method]
		if !ok {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	}
}

// closedEndpoint returns an endpoint on a port nothing listens on: a
// listener is opened on port 0 to learn a free port and closed again.
func closedEndpoint(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return "http://" + addr
}

// testConfig returns a finalized Config aimed at endpoint with retries off,
// so a scripted failure surfaces on the first try. options overlays further
// provider settings.
func testConfig(t *testing.T, endpoint string, options map[string]string) storage.Config {
	t.Helper()
	cfg := storage.Config{
		Endpoint:  endpoint,
		Container: testBucket,
		Account:   testAccount,
		Key:       testKey,
		Options:   map[string]string{"max_retries": "0"},
	}
	maps.Copy(cfg.Options, options)
	if err := cfg.Finalize(""); err != nil {
		t.Fatalf("finalize config: %v", err)
	}
	return cfg
}

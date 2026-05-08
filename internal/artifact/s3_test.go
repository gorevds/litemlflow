package artifact_test

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/litemlflow/litemlflow/internal/artifact"
)

// ---- minimal in-process S3 mock --------------------------------------------

type s3Object struct {
	data []byte
}

type mockS3 struct {
	mu      sync.Mutex
	objects map[string]*s3Object // key → object
}

func newMockS3() *mockS3 {
	return &mockS3{objects: make(map[string]*s3Object)}
}

// xmlListBucketResult is the minimal XML envelope for ListObjectsV2.
type xmlListBucketResult struct {
	XMLName        xml.Name          `xml:"ListBucketResult"`
	IsTruncated    bool              `xml:"IsTruncated"`
	Contents       []xmlListContents `xml:"Contents"`
	CommonPrefixes []xmlCommonPrefix `xml:"CommonPrefixes"`
}

type xmlListContents struct {
	Key  string `xml:"Key"`
	Size int    `xml:"Size"`
}

type xmlCommonPrefix struct {
	Prefix string `xml:"Prefix"`
}

// ServeHTTP dispatches to the appropriate handler based on method and path.
// All requests arrive as path-style: /<bucket>/<key>
func (m *mockS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Strip leading slash and split bucket/key.
	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(path, "/", 2)
	// parts[0] = bucket, parts[1] = key (may be "")

	key := ""
	if len(parts) == 2 {
		key = parts[1]
	}

	// Query params.
	q := r.URL.Query()
	isList := q.Get("list-type") == "2"

	switch {
	case r.Method == http.MethodGet && isList:
		m.handleList(w, r, q)
	case r.Method == http.MethodGet:
		m.handleGet(w, key)
	case r.Method == http.MethodPut:
		m.handlePut(w, r, key)
	case r.Method == http.MethodDelete:
		m.handleDelete(w, key)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (m *mockS3) handlePut(w http.ResponseWriter, r *http.Request, key string) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	m.mu.Lock()
	m.objects[key] = &s3Object{data: data}
	m.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (m *mockS3) handleGet(w http.ResponseWriter, key string) {
	m.mu.Lock()
	obj, ok := m.objects[key]
	m.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Length", intStr(int64(len(obj.data))))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(obj.data)
}

func (m *mockS3) handleDelete(w http.ResponseWriter, key string) {
	m.mu.Lock()
	_, ok := m.objects[key]
	if ok {
		delete(m.objects, key)
	}
	m.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *mockS3) handleList(w http.ResponseWriter, r *http.Request, q url.Values) {
	prefix := q.Get("prefix")
	delimiter := q.Get("delimiter")

	m.mu.Lock()
	defer m.mu.Unlock()

	var contents []xmlListContents
	prefixSet := make(map[string]bool)

	for key, obj := range m.objects {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		// Check for a delimiter-bounded sub-prefix (simulates common prefixes).
		rest := key[len(prefix):]
		if delimiter != "" {
			if idx := strings.Index(rest, delimiter); idx >= 0 {
				// This key lives under a sub-"directory".
				subPrefix := prefix + rest[:idx+len(delimiter)]
				prefixSet[subPrefix] = true
				continue
			}
		}
		contents = append(contents, xmlListContents{Key: key, Size: len(obj.data)})
	}

	var commonPrefixes []xmlCommonPrefix
	for p := range prefixSet {
		commonPrefixes = append(commonPrefixes, xmlCommonPrefix{Prefix: p})
	}

	result := xmlListBucketResult{
		IsTruncated:    false,
		Contents:       contents,
		CommonPrefixes: commonPrefixes,
	}
	data, _ := xml.Marshal(result)

	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	_, _ = w.Write(data)
}

// ---- helpers ----------------------------------------------------------------

func intStr(n int64) string {
	return fmt.Sprintf("%d", n)
}

// newTestStore creates an S3Store wired to the given httptest server.
func newTestStore(t *testing.T, srv *httptest.Server) *artifact.S3Store {
	t.Helper()
	s, err := artifact.NewS3Store(artifact.S3Config{
		Endpoint:  srv.URL,
		Bucket:    "testbucket",
		Region:    "us-east-1",
		AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		HTTP:      srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}
	return s
}

// ---- tests ------------------------------------------------------------------

func TestS3UploadAndOpen(t *testing.T) {
	t.Parallel()
	mock := newMockS3()
	srv := httptest.NewServer(mock)
	defer srv.Close()

	s := newTestStore(t, srv)

	const content = "hello s3"
	if err := s.Upload("run1", "model/weights.bin", strings.NewReader(content), 0); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	rc, size, err := s.Open("run1", "model/weights.bin")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()

	if size != int64(len(content)) {
		t.Fatalf("size: want %d got %d", len(content), size)
	}
	got, _ := io.ReadAll(rc)
	if string(got) != content {
		t.Fatalf("content mismatch: %q", got)
	}
}

func TestS3OpenNotFound(t *testing.T) {
	t.Parallel()
	mock := newMockS3()
	srv := httptest.NewServer(mock)
	defer srv.Close()

	s := newTestStore(t, srv)
	_, _, err := s.Open("run1", "missing.txt")
	if !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestS3DeleteFile(t *testing.T) {
	t.Parallel()
	mock := newMockS3()
	srv := httptest.NewServer(mock)
	defer srv.Close()

	s := newTestStore(t, srv)
	_ = s.Upload("run1", "f.txt", strings.NewReader("x"), 0)

	if err := s.Delete("run1", "f.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	// Second delete → ErrNotFound.
	if err := s.Delete("run1", "f.txt"); !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("want ErrNotFound after double delete, got %v", err)
	}
}

func TestS3DeleteDirectory(t *testing.T) {
	t.Parallel()
	mock := newMockS3()
	srv := httptest.NewServer(mock)
	defer srv.Close()

	s := newTestStore(t, srv)
	_ = s.Upload("run1", "dir/a.txt", strings.NewReader("a"), 0)
	_ = s.Upload("run1", "dir/b.txt", strings.NewReader("b"), 0)

	if err := s.Delete("run1", "dir"); err != nil {
		t.Fatalf("Delete dir: %v", err)
	}

	// Both files should be gone.
	_, _, err := s.Open("run1", "dir/a.txt")
	if !errors.Is(err, artifact.ErrNotFound) {
		t.Fatalf("a.txt should be gone, got %v", err)
	}
}

func TestS3List(t *testing.T) {
	t.Parallel()
	mock := newMockS3()
	srv := httptest.NewServer(mock)
	defer srv.Close()

	s := newTestStore(t, srv)
	_ = s.Upload("run1", "a.txt", strings.NewReader("a"), 0)
	_ = s.Upload("run1", "sub/b.txt", strings.NewReader("b"), 0)
	_ = s.Upload("run1", "sub/c.txt", strings.NewReader("c"), 0)

	entries, err := s.List("run1", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Expect 2: "a.txt" (file) and "sub" (dir)
	if len(entries) != 2 {
		t.Fatalf("want 2 top-level entries, got %d: %v", len(entries), entries)
	}

	subEntries, err := s.List("run1", "sub")
	if err != nil {
		t.Fatalf("List sub: %v", err)
	}
	if len(subEntries) != 2 {
		t.Fatalf("want 2 sub entries, got %d: %v", len(subEntries), subEntries)
	}
}

func TestS3SizeCap(t *testing.T) {
	t.Parallel()
	mock := newMockS3()
	srv := httptest.NewServer(mock)
	defer srv.Close()

	s := newTestStore(t, srv)
	big := strings.Repeat("x", 1024)
	err := s.Upload("run1", "f", strings.NewReader(big), 100)
	if !errors.Is(err, artifact.ErrPayloadTooBig) {
		t.Fatalf("want ErrPayloadTooBig, got %v", err)
	}
}

func TestS3PathTraversalRunID(t *testing.T) {
	t.Parallel()
	mock := newMockS3()
	srv := httptest.NewServer(mock)
	defer srv.Close()

	s := newTestStore(t, srv)
	for _, bad := range []string{"", "../escape", "run/with/slash", "back\\slash"} {
		err := s.Upload(bad, "f", strings.NewReader("x"), 0)
		if !errors.Is(err, artifact.ErrInvalidPath) {
			t.Fatalf("runID %q should be rejected, got %v", bad, err)
		}
	}
}

func TestS3AuthorizationHeaderFormat(t *testing.T) {
	t.Parallel()

	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s, err := artifact.NewS3Store(artifact.S3Config{
		Endpoint:  srv.URL,
		Bucket:    "mybucket",
		Region:    "us-east-1",
		AccessKey: "AKIATEST",
		SecretKey: "mysecret",
		HTTP:      srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}

	// A simple upload so we get a real signed request.
	_ = s.Upload("run42", "test.txt", bytes.NewReader([]byte("hi")), 0)

	if capturedAuth == "" {
		t.Fatal("no Authorization header captured")
	}
	if !strings.HasPrefix(capturedAuth, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("Authorization header has wrong prefix: %q", capturedAuth)
	}
	// Verify structure: must contain Credential=, SignedHeaders=, Signature=.
	for _, field := range []string{"Credential=", "SignedHeaders=", "Signature="} {
		if !strings.Contains(capturedAuth, field) {
			t.Fatalf("Authorization header missing %q: %s", field, capturedAuth)
		}
	}
	// Credential must include our access key and the signing scope.
	if !strings.Contains(capturedAuth, "AKIATEST/") {
		t.Fatalf("Authorization does not contain access key: %s", capturedAuth)
	}
	if !strings.Contains(capturedAuth, "/us-east-1/s3/aws4_request") {
		t.Fatalf("Authorization does not contain correct scope: %s", capturedAuth)
	}
	// x-amz-date must be in signed headers.
	if !strings.Contains(capturedAuth, "x-amz-date") {
		t.Fatalf("Authorization does not sign x-amz-date: %s", capturedAuth)
	}
}

// TestS3RealMinIO is an integration test that only runs when
// LITEMLFLOW_S3_TEST_ENDPOINT is set to a real MinIO endpoint.
func TestS3RealMinIO(t *testing.T) {
	ep := os.Getenv("LITEMLFLOW_S3_TEST_ENDPOINT")
	if ep == "" {
		t.Skip("set LITEMLFLOW_S3_TEST_ENDPOINT to run against real MinIO")
	}
	bucket := os.Getenv("LITEMLFLOW_S3_TEST_BUCKET")
	if bucket == "" {
		bucket = "litemlflow-test"
	}

	s, err := artifact.NewS3Store(artifact.S3Config{
		Endpoint:  ep,
		Bucket:    bucket,
		Region:    envOrDefault("LITEMLFLOW_S3_TEST_REGION", "us-east-1"),
		AccessKey: envOrDefault("LITEMLFLOW_S3_TEST_ACCESS_KEY", "minioadmin"),
		SecretKey: envOrDefault("LITEMLFLOW_S3_TEST_SECRET_KEY", "minioadmin"),
	})
	if err != nil {
		t.Fatalf("NewS3Store: %v", err)
	}

	const runID = "integ-test-run-001"
	const content = "integration test payload"

	// Upload.
	if err := s.Upload(runID, "integ/file.txt", strings.NewReader(content), 0); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	// Open + read.
	rc, size, err := s.Open(runID, "integ/file.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer rc.Close()
	if size != int64(len(content)) {
		t.Errorf("size: want %d got %d", len(content), size)
	}
	got, _ := io.ReadAll(rc)
	if string(got) != content {
		t.Errorf("content mismatch: %q", got)
	}

	// List.
	entries, err := s.List(runID, "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	t.Logf("listed %d entries: %v", len(entries), entries)

	// Delete.
	if err := s.Delete(runID, "integ/file.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

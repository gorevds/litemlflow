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

	"github.com/gorevds/litemlflow/internal/artifact"
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

// newTestStoreWithThreshold creates an S3Store with a custom multipart threshold.
func newTestStoreWithThreshold(t *testing.T, srv *httptest.Server, threshold int64) *artifact.S3Store {
	t.Helper()
	s, err := artifact.NewS3Store(artifact.S3Config{
		Endpoint:           srv.URL,
		Bucket:             "testbucket",
		Region:             "us-east-1",
		AccessKey:          "AKIAIOSFODNN7EXAMPLE",
		SecretKey:          "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		MultipartThreshold: threshold,
		HTTP:               srv.Client(),
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

// ---- multipart upload tests -------------------------------------------------

// mockMultipartS3 is a specialised mock that tracks the sequence of S3
// multipart API calls so tests can verify ordering and query parameters.
type mockMultipartS3 struct {
	mu sync.Mutex

	// Recorded operations in order of receipt.
	ops []string // "initiate", "part:N", "complete", "abort"

	// Assembled object data, keyed by key.
	objects map[string][]byte

	// In-progress multipart: parts collected keyed by uploadID.
	parts map[string]map[int][]byte

	// failPartNumber, when > 0, causes uploadPart to return 500 for that part.
	failPartNumber int
}

func newMockMultipartS3() *mockMultipartS3 {
	return &mockMultipartS3{
		objects: make(map[string][]byte),
		parts:   make(map[string]map[int][]byte),
	}
}

func (m *mockMultipartS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(path, "/", 2)
	key := ""
	if len(parts) == 2 {
		key = parts[1]
	}
	q := r.URL.Query()

	m.mu.Lock()
	defer m.mu.Unlock()

	switch {
	// Initiate multipart: POST /{bucket}/{key}?uploads
	case r.Method == http.MethodPost && q.Get("uploads") == "" && q.Has("uploads"):
		// S3 sends ?uploads= (empty value), url.Values parses it with key "uploads" and value ""
		uploadID := fmt.Sprintf("upload-%d", len(m.parts)+1)
		m.parts[uploadID] = make(map[int][]byte)
		m.ops = append(m.ops, "initiate")
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w,
			`<?xml version="1.0" encoding="UTF-8"?>`+
				`<InitiateMultipartUploadResult><Bucket>testbucket</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>`,
			key, uploadID)

	// Upload part: PUT /{bucket}/{key}?partNumber=N&uploadId=<id>
	case r.Method == http.MethodPut && q.Get("partNumber") != "" && q.Get("uploadId") != "":
		uploadID := q.Get("uploadId")
		partNum := 0
		_, _ = fmt.Sscanf(q.Get("partNumber"), "%d", &partNum)
		if m.failPartNumber > 0 && partNum == m.failPartNumber {
			m.ops = append(m.ops, fmt.Sprintf("part:%d:fail", partNum))
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("injected failure"))
			return
		}
		data, _ := io.ReadAll(r.Body)
		if m.parts[uploadID] == nil {
			m.parts[uploadID] = make(map[int][]byte)
		}
		m.parts[uploadID][partNum] = data
		m.ops = append(m.ops, fmt.Sprintf("part:%d", partNum))
		etag := fmt.Sprintf(`"etag-part-%d"`, partNum)
		w.Header().Set("ETag", etag)
		w.WriteHeader(http.StatusOK)

	// Complete multipart: POST /{bucket}/{key}?uploadId=<id>
	case r.Method == http.MethodPost && q.Get("uploadId") != "" && !q.Has("uploads"):
		uploadID := q.Get("uploadId")
		// Assemble parts in order.
		partMap := m.parts[uploadID]
		var assembled []byte
		for i := 1; i <= len(partMap); i++ {
			assembled = append(assembled, partMap[i]...)
		}
		m.objects[key] = assembled
		delete(m.parts, uploadID)
		m.ops = append(m.ops, "complete")
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w,
			`<?xml version="1.0" encoding="UTF-8"?>`+
				`<CompleteMultipartUploadResult><Key>`+key+`</Key></CompleteMultipartUploadResult>`)

	// Abort multipart: DELETE /{bucket}/{key}?uploadId=<id>
	case r.Method == http.MethodDelete && q.Get("uploadId") != "":
		uploadID := q.Get("uploadId")
		delete(m.parts, uploadID)
		m.ops = append(m.ops, "abort")
		w.WriteHeader(http.StatusNoContent)

	// Regular single PUT
	case r.Method == http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		m.objects[key] = data
		m.ops = append(m.ops, "single-put")
		w.WriteHeader(http.StatusOK)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// TestS3MultipartLargeUpload verifies that uploads over the threshold trigger
// the multipart API in the correct order (initiate → parts → complete).
func TestS3MultipartLargeUpload(t *testing.T) {
	t.Parallel()

	// Threshold of 1 KiB makes this test fast; we send 2.5 KiB → 3 parts.
	const threshold = 1024
	payload := bytes.Repeat([]byte("M"), 2*threshold+threshold/2) // 2560 bytes → 3 parts

	mock := newMockMultipartS3()
	srv := httptest.NewServer(mock)
	defer srv.Close()

	s := newTestStoreWithThreshold(t, srv, threshold)

	if err := s.Upload("runX", "big/file.bin", bytes.NewReader(payload), 0); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	mock.mu.Lock()
	ops := append([]string(nil), mock.ops...)
	assembled := append([]byte(nil), mock.objects["artifacts/runX/big/file.bin"]...)
	mock.mu.Unlock()

	// Verify operation sequence: initiate → part:1 → part:2 → part:3 → complete
	wantOps := []string{"initiate", "part:1", "part:2", "part:3", "complete"}
	if len(ops) != len(wantOps) {
		t.Fatalf("want ops %v, got %v", wantOps, ops)
	}
	for i, op := range wantOps {
		if ops[i] != op {
			t.Errorf("op[%d]: want %q got %q", i, op, ops[i])
		}
	}

	// Verify data integrity.
	if !bytes.Equal(assembled, payload) {
		t.Fatalf("assembled data mismatch: len want %d got %d", len(payload), len(assembled))
	}
}

// TestS3SinglePutBelowThreshold verifies that uploads below the threshold
// use a plain single PUT (no multipart API calls).
func TestS3SinglePutBelowThreshold(t *testing.T) {
	t.Parallel()

	const threshold = 1024
	payload := bytes.Repeat([]byte("S"), threshold-1) // 1 byte below threshold

	mock := newMockMultipartS3()
	srv := httptest.NewServer(mock)
	defer srv.Close()

	s := newTestStoreWithThreshold(t, srv, threshold)

	if err := s.Upload("runY", "small.txt", bytes.NewReader(payload), 0); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	mock.mu.Lock()
	ops := append([]string(nil), mock.ops...)
	mock.mu.Unlock()

	if len(ops) != 1 || ops[0] != "single-put" {
		t.Fatalf("expected single-put, got %v", ops)
	}
}

// TestS3MultipartAbortOnPartFailure verifies that when a part upload fails,
// abortMultipart is called so orphaned parts are cleaned up.
func TestS3MultipartAbortOnPartFailure(t *testing.T) {
	t.Parallel()

	const threshold = 512
	payload := bytes.Repeat([]byte("A"), 3*threshold) // 3 parts; part 2 will fail

	mock := newMockMultipartS3()
	mock.failPartNumber = 2
	srv := httptest.NewServer(mock)
	defer srv.Close()

	s := newTestStoreWithThreshold(t, srv, threshold)

	err := s.Upload("runZ", "fail.bin", bytes.NewReader(payload), 0)
	if err == nil {
		t.Fatal("expected an error from the failing part, got nil")
	}

	mock.mu.Lock()
	ops := append([]string(nil), mock.ops...)
	mock.mu.Unlock()

	// Must see: initiate, part:1, part:2:fail, abort — in that order.
	if len(ops) < 4 {
		t.Fatalf("expected at least 4 ops (initiate/part1/part2fail/abort), got %v", ops)
	}
	if ops[0] != "initiate" {
		t.Errorf("first op: want initiate, got %q", ops[0])
	}
	if ops[len(ops)-1] != "abort" {
		t.Errorf("last op: want abort, got %q", ops[len(ops)-1])
	}
}

// TestS3MultipartCompleteXMLFormat verifies the CompleteMultipartUpload XML
// body sent by the client has the required shape:
//
//	<CompleteMultipartUpload>
//	  <Part><PartNumber>1</PartNumber><ETag>"abc..."</ETag></Part>
//	  ...
//	</CompleteMultipartUpload>
func TestS3MultipartCompleteXMLFormat(t *testing.T) {
	t.Parallel()

	const threshold = 512
	payload := bytes.Repeat([]byte("X"), 2*threshold) // exactly 2 parts

	// Capture the raw CompleteMultipartUpload request body.
	var (
		capturedBody string
		capturedMu   sync.Mutex
	)

	// We need a real multipart mock that also captures the complete body.
	type xmlPart struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
	}
	type xmlComplete struct {
		XMLName xml.Name  `xml:"CompleteMultipartUpload"`
		Parts   []xmlPart `xml:"Part"`
	}

	uploadID := "test-upload-id-xml"
	partETags := map[int]string{}
	var partETagsMu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.SplitN(path, "/", 2)
		key := ""
		if len(parts) == 2 {
			key = parts[1]
		}
		q := r.URL.Query()
		_ = key

		switch {
		case r.Method == http.MethodPost && q.Has("uploads"):
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w,
				`<?xml version="1.0" encoding="UTF-8"?>`+
					`<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>`,
				uploadID)

		case r.Method == http.MethodPut && q.Get("partNumber") != "":
			pn := 0
			_, _ = fmt.Sscanf(q.Get("partNumber"), "%d", &pn)
			_, _ = io.ReadAll(r.Body)
			etag := fmt.Sprintf(`"etag-%d-abcdef"`, pn)
			partETagsMu.Lock()
			partETags[pn] = etag
			partETagsMu.Unlock()
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusOK)

		case r.Method == http.MethodPost && q.Get("uploadId") != "":
			body, _ := io.ReadAll(r.Body)
			capturedMu.Lock()
			capturedBody = string(body)
			capturedMu.Unlock()
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w,
				`<?xml version="1.0" encoding="UTF-8"?>`+
					`<CompleteMultipartUploadResult><Key>k</Key></CompleteMultipartUploadResult>`)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	s := newTestStoreWithThreshold(t, srv, threshold)
	if err := s.Upload("runXML", "data.bin", bytes.NewReader(payload), 0); err != nil {
		t.Fatalf("Upload: %v", err)
	}

	capturedMu.Lock()
	body := capturedBody
	capturedMu.Unlock()

	if body == "" {
		t.Fatal("complete request body was empty")
	}

	var parsed xmlComplete
	if err := xml.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("parse CompleteMultipartUpload XML: %v\nbody: %s", err, body)
	}
	if len(parsed.Parts) != 2 {
		t.Fatalf("want 2 parts in XML, got %d\nbody: %s", len(parsed.Parts), body)
	}
	for i, p := range parsed.Parts {
		wantNum := i + 1
		if p.PartNumber != wantNum {
			t.Errorf("part[%d]: PartNumber want %d got %d", i, wantNum, p.PartNumber)
		}
		partETagsMu.Lock()
		wantETag := partETags[wantNum]
		partETagsMu.Unlock()
		if p.ETag != wantETag {
			t.Errorf("part[%d]: ETag want %q got %q", i, wantETag, p.ETag)
		}
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

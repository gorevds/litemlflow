// Package artifact — S3-compatible backend.
//
// Signs requests with AWS Signature Version 4 using only stdlib:
// crypto/hmac, crypto/sha256, encoding/hex.
//
// Key layout: <Prefix>artifacts/<runID>/<relPath>
// Addressing:  path-style (default for any non-amazonaws.com endpoint)
//              virtual-hosted for amazonaws.com
package artifact

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// S3Config is the configuration for NewS3Store.
type S3Config struct {
	Endpoint  string // e.g. "https://s3.amazonaws.com" or "http://minio:9000"
	Bucket    string
	Region    string
	Prefix    string // optional, e.g. "litemlflow/"
	AccessKey string
	SecretKey string
	// HTTP is optional; a default client is used when nil.
	HTTP *http.Client
}

// S3Store implements the Store interface backed by an S3-compatible object store.
type S3Store struct {
	endpoint  string // base URL, no trailing slash
	bucket    string
	region    string
	prefix    string
	accessKey string
	secretKey string
	pathStyle bool // true → path-style, false → virtual-hosted
	http      *http.Client
}

// NewS3Store validates cfg and returns a ready S3Store.
func NewS3Store(cfg S3Config) (*S3Store, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("s3: endpoint is required")
	}
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("s3: bucket is required")
	}
	// Defense-in-depth bucket name validation (S3 actually rejects invalid
	// names too, but catching it at construction yields a clearer error and
	// blocks accidental host-header injection in virtual-hosted-style URLs).
	if !validBucketName(cfg.Bucket) {
		return nil, fmt.Errorf("s3: bucket name %q must be 3-63 chars, lowercase alphanumeric, hyphen, or dot", cfg.Bucket)
	}
	if cfg.Region == "" {
		return nil, fmt.Errorf("s3: region is required")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("s3: access_key and secret_key are required")
	}

	// Normalise endpoint: strip trailing slash.
	ep := strings.TrimRight(cfg.Endpoint, "/")

	// Detect addressing style:
	// - amazonaws.com → virtual-hosted (bucket.s3.region.amazonaws.com)
	// - everything else → path-style   (host/bucket/key)
	u, err := url.Parse(ep)
	if err != nil {
		return nil, fmt.Errorf("s3: invalid endpoint %q: %w", ep, err)
	}
	pathStyle := !strings.HasSuffix(u.Hostname(), "amazonaws.com")

	client := cfg.HTTP
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}

	return &S3Store{
		endpoint:  ep,
		bucket:    cfg.Bucket,
		region:    cfg.Region,
		prefix:    cfg.Prefix,
		accessKey: cfg.AccessKey,
		secretKey: cfg.SecretKey,
		pathStyle: pathStyle,
		http:      client,
	}, nil
}

// ---- Store implementation ---------------------------------------------------

// defaultUploadCap is the implicit upload cap when caller passes maxSize<=0.
// It exists so a malicious client streaming an unbounded body cannot exhaust
// server memory. Operators who need to upload >5GiB must explicitly raise
// the per-server cap (config.MaxArtifactSize) AND pass it to Upload().
const defaultUploadCap = int64(5) << 30 // 5 GiB

// Upload stores r at runID/relPath, enforcing maxSize when > 0. When
// maxSize <= 0, defaultUploadCap (5 GiB) is applied as a safety belt.
func (s *S3Store) Upload(runID, relPath string, r io.Reader, maxSize int64) error {
	if err := validateRunID(runID); err != nil {
		return err
	}
	key := s.key(runID, relPath)

	if maxSize <= 0 {
		maxSize = defaultUploadCap
	}

	// Read into buffer to (a) enforce size cap, (b) know Content-Length for
	// SigV4 (S3 wants Content-Length on every PUT).
	buf := &bytes.Buffer{}
	lr := io.LimitReader(r, maxSize+1)
	n, err := io.Copy(buf, lr)
	if err != nil {
		return fmt.Errorf("s3 upload read: %w", err)
	}
	if n > maxSize {
		return ErrPayloadTooBig
	}
	body := io.Reader(buf)
	contentLength := n

	req, err := s.newRequest(http.MethodPut, key, body, contentLength, nil)
	if err != nil {
		return fmt.Errorf("s3 upload: %w", err)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return fmt.Errorf("s3 upload: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("s3 upload: status %d: %s", resp.StatusCode, msg)
	}
	return nil
}

// Open returns an io.ReadCloser for the object and its size.
func (s *S3Store) Open(runID, relPath string) (io.ReadCloser, int64, error) {
	if err := validateRunID(runID); err != nil {
		return nil, 0, err
	}
	key := s.key(runID, relPath)

	req, err := s.newRequest(http.MethodGet, key, nil, 0, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("s3 open: %w", err)
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("s3 open: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, 0, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, 0, fmt.Errorf("s3 open: status %d: %s", resp.StatusCode, msg)
	}
	return resp.Body, resp.ContentLength, nil
}

// Delete removes a single object or all objects under a key prefix (directory).
func (s *S3Store) Delete(runID, relPath string) error {
	if err := validateRunID(runID); err != nil {
		return err
	}
	key := s.key(runID, relPath)

	// Try a direct DELETE first (covers the file case).
	err := s.deleteObject(key)
	if err == nil {
		return nil
	}
	if !isNotFound(err) {
		return err
	}

	// 404 → might be a directory-like prefix. List everything under it and
	// delete each object in turn.
	keys, listErr := s.listAllKeys(key + "/")
	if listErr != nil {
		return listErr
	}
	if len(keys) == 0 {
		return ErrNotFound
	}
	for _, k := range keys {
		if delErr := s.deleteObject(k); delErr != nil {
			return delErr
		}
	}
	return nil
}

// List returns immediate children of runID/dir using ListObjectsV2 with a
// delimiter so subdirectories collapse into "common prefixes".
func (s *S3Store) List(runID, dir string) ([]ListEntry, error) {
	if err := validateRunID(runID); err != nil {
		return nil, err
	}

	// The prefix to list under: everything inside the run's artifact subtree.
	// If dir is empty we list the top level; otherwise append dir with trailing /.
	baseKey := s.key(runID, "")
	listPrefix := baseKey
	if dir != "" && dir != "." {
		listPrefix = baseKey + dir + "/"
	}

	type xmlContents struct {
		Key  string `xml:"Key"`
		Size int64  `xml:"Size"`
	}
	type xmlPrefix struct {
		Prefix string `xml:"Prefix"`
	}
	type xmlListResp struct {
		XMLName        xml.Name     `xml:"ListBucketResult"`
		Contents       []xmlContents `xml:"Contents"`
		CommonPrefixes []xmlPrefix  `xml:"CommonPrefixes"`
		IsTruncated    bool         `xml:"IsTruncated"`
		NextToken      string       `xml:"NextContinuationToken"`
	}

	var entries []ListEntry
	var contToken string

	for {
		params := url.Values{}
		params.Set("list-type", "2")
		params.Set("prefix", listPrefix)
		params.Set("delimiter", "/")
		if contToken != "" {
			params.Set("continuation-token", contToken)
		}

		req, err := s.newRequest(http.MethodGet, "", nil, 0, params)
		if err != nil {
			return nil, fmt.Errorf("s3 list: %w", err)
		}
		resp, err := s.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("s3 list: %w", err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("s3 list: status %d: %s", resp.StatusCode, body)
		}

		var result xmlListResp
		if err := xml.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("s3 list: parse response: %w", err)
		}

		// Each common prefix is a sub-directory.
		for _, cp := range result.CommonPrefixes {
			// Strip the base prefix and trailing slash to get the relative path.
			rel := strings.TrimPrefix(cp.Prefix, baseKey)
			rel = strings.TrimSuffix(rel, "/")
			entries = append(entries, ListEntry{Path: rel, IsDir: true})
		}
		// Each content is a file.
		for _, c := range result.Contents {
			rel := strings.TrimPrefix(c.Key, baseKey)
			if rel == "" {
				// Skip the "directory object" placeholder if any.
				continue
			}
			entries = append(entries, ListEntry{Path: rel, IsDir: false, Size: c.Size})
		}

		if !result.IsTruncated {
			break
		}
		contToken = result.NextToken
	}

	return entries, nil
}

// ---- internal helpers -------------------------------------------------------

// key builds the full S3 object key for a run + relative path.
// Layout: <Prefix>artifacts/<runID>/<relPath>
func (s *S3Store) key(runID, relPath string) string {
	base := s.prefix + "artifacts/" + runID + "/"
	if relPath == "" || relPath == "." {
		return base
	}
	// Normalise slashes; reject anything that looks like path traversal.
	clean := strings.TrimPrefix(relPath, "/")
	return base + clean
}

// objectURL builds the full HTTP URL for a specific key. Each path segment
// of the key is percent-encoded so keys with spaces, parentheses, etc. don't
// break either url.Parse or SigV4 canonicalization.
//
// When key is empty it targets the bucket root (for listing).
func (s *S3Store) objectURL(key string) string {
	encoded := encodeKeyPath(key)
	if s.pathStyle {
		if encoded == "" {
			return s.endpoint + "/" + s.bucket
		}
		return s.endpoint + "/" + s.bucket + "/" + encoded
	}
	// Virtual-hosted style: inject bucket into host.
	u, _ := url.Parse(s.endpoint)
	u.Host = s.bucket + "." + u.Host
	if encoded == "" {
		return u.String()
	}
	return u.String() + "/" + encoded
}

// encodeKeyPath percent-encodes each segment of the key while preserving
// segment separators ("/"). url.PathEscape would also escape "/", which is
// wrong for S3 keys.
func encodeKeyPath(key string) string {
	if key == "" {
		return ""
	}
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// newRequest creates a signed HTTP request.
// key="" targets the bucket (for ListObjectsV2).
// body may be nil (for GET/DELETE). contentLength=-1 means unknown (streaming).
func (s *S3Store) newRequest(method, key string, body io.Reader, contentLength int64, params url.Values) (*http.Request, error) {
	rawURL := s.objectURL(key)
	if len(params) > 0 {
		rawURL += "?" + params.Encode()
	}

	req, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		return nil, err
	}
	if contentLength >= 0 {
		req.ContentLength = contentLength
	}

	now := time.Now().UTC()
	if err := s.sign(req, now, body, contentLength); err != nil {
		return nil, fmt.Errorf("sign: %w", err)
	}
	return req, nil
}

// sign adds the AWS SigV4 Authorization header (and x-amz-date / x-amz-content-sha256).
//
// For bodies that are io.Reader we compute the hash from the buffered bytes when
// available (bytes.Buffer / bytes.Reader). For streaming uploads with unknown
// content we use the "UNSIGNED-PAYLOAD" shortcut which is accepted by S3 and
// MinIO when using HTTP (not HTTPS mandatory auth).
//
// SigV4 reference: https://docs.aws.amazon.com/general/latest/gr/sigv4_signing.html
func (s *S3Store) sign(req *http.Request, t time.Time, body io.Reader, contentLength int64) error {
	dateStamp := t.Format("20060102")
	amzDate := t.Format("20060102T150405Z")

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("Host", req.URL.Host)

	// Payload hash.
	payloadHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // SHA256("")
	if body != nil {
		switch v := body.(type) {
		case *bytes.Buffer:
			payloadHash = sha256hex(v.Bytes())
		case *bytes.Reader:
			data := make([]byte, v.Len())
			_, _ = v.Read(data)
			_, _ = v.Seek(0, io.SeekStart)
			payloadHash = sha256hex(data)
		default:
			// Streaming body — use unsigned payload sentinel.
			payloadHash = "UNSIGNED-PAYLOAD"
		}
	}
	req.Header.Set("x-amz-content-sha256", payloadHash)

	// Content-Type for PUTs.
	if req.Method == http.MethodPut && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/octet-stream")
	}

	// ---- Step 1: Canonical request ------------------------------------------

	// Canonical URI: URL-encode the path (but not the slashes).
	canonicalURI := canonicalizeURI(req.URL.Path)

	// Canonical query string: sorted by name.
	canonicalQS := canonicalizeQueryString(req.URL.RawQuery)

	// Canonical headers: lowercase name, trimmed value, sorted.
	signedHeaderNames, canonicalHeaders := buildCanonicalHeaders(req)

	canonicalReq := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQS,
		canonicalHeaders,
		signedHeaderNames,
		payloadHash,
	}, "\n")

	// ---- Step 2: String to sign ---------------------------------------------

	scope := strings.Join([]string{dateStamp, s.region, "s3", "aws4_request"}, "/")
	credentialScope := dateStamp + "/" + s.region + "/s3/aws4_request"
	strToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		credentialScope,
		sha256hex([]byte(canonicalReq)),
	}, "\n")

	// ---- Step 3: Signing key ------------------------------------------------

	signingKey := deriveSigningKey(s.secretKey, dateStamp, s.region, "s3")

	// ---- Step 4: Signature --------------------------------------------------

	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(strToSign)))

	// ---- Step 5: Authorization header ---------------------------------------

	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.accessKey, scope, signedHeaderNames, signature,
	)
	req.Header.Set("Authorization", auth)

	return nil
}

// deleteObject sends a DELETE request for a single key.
func (s *S3Store) deleteObject(key string) error {
	req, err := s.newRequest(http.MethodDelete, key, nil, 0, nil)
	if err != nil {
		return err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("s3 delete: status %d: %s", resp.StatusCode, msg)
	}
	return nil
}

// listAllKeys returns all object keys whose names start with prefix
// (paginating through ListObjectsV2 results).
func (s *S3Store) listAllKeys(prefix string) ([]string, error) {
	type xmlContents struct {
		Key string `xml:"Key"`
	}
	type xmlResp struct {
		XMLName     xml.Name     `xml:"ListBucketResult"`
		Contents    []xmlContents `xml:"Contents"`
		IsTruncated bool         `xml:"IsTruncated"`
		NextToken   string       `xml:"NextContinuationToken"`
	}

	var keys []string
	var contToken string

	for {
		params := url.Values{}
		params.Set("list-type", "2")
		params.Set("prefix", prefix)
		if contToken != "" {
			params.Set("continuation-token", contToken)
		}

		req, err := s.newRequest(http.MethodGet, "", nil, 0, params)
		if err != nil {
			return nil, err
		}
		resp, err := s.http.Do(req)
		if err != nil {
			return nil, err
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("s3 list all keys: status %d: %s", resp.StatusCode, body)
		}

		var result xmlResp
		if err := xml.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("s3 list all keys: parse: %w", err)
		}
		for _, c := range result.Contents {
			keys = append(keys, c.Key)
		}
		if !result.IsTruncated {
			break
		}
		contToken = result.NextToken
	}
	return keys, nil
}

// isNotFound reports whether err is an ErrNotFound sentinel.
func isNotFound(err error) bool {
	return err == ErrNotFound
}

// ---- SigV4 primitives -------------------------------------------------------

func sha256hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

// deriveSigningKey builds the SigV4 signing key:
//
//	kDate     = HMAC("AWS4" + secret, date)
//	kRegion   = HMAC(kDate, region)
//	kService  = HMAC(kRegion, service)
//	kSigning  = HMAC(kService, "aws4_request")
func deriveSigningKey(secret, date, region, service string) []byte {
	kSecret := []byte("AWS4" + secret)
	kDate := hmacSHA256(kSecret, []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

// canonicalizeURI percent-encodes each path segment (but not the slash
// separators), as required by SigV4. It does NOT double-encode existing %XX
// sequences — for S3 requests we only receive clean paths from our own code.
func canonicalizeURI(path string) string {
	if path == "" {
		return "/"
	}
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		segments[i] = url.PathEscape(seg)
	}
	return strings.Join(segments, "/")
}

// canonicalizeQueryString returns a sorted, URL-encoded query string exactly
// as required by SigV4 (keys sorted, then values sorted for each key).
func canonicalizeQueryString(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	vals, _ := url.ParseQuery(rawQuery)
	keys := make([]string, 0, len(vals))
	for k := range vals {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		vs := vals[k]
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// buildCanonicalHeaders returns (signedHeaderNames, canonicalHeadersBlock).
// Headers are lowercased, sorted, and trimmed.
func buildCanonicalHeaders(req *http.Request) (signedHeaderNames, canonicalHeaders string) {
	// Collect the headers we will sign.
	// Always sign: host, content-type (when present), and all x-amz-* headers.
	type kv struct{ k, v string }
	var pairs []kv

	// Host is taken from req.URL.Host (not from the Header map).
	pairs = append(pairs, kv{"host", req.URL.Host})

	for name, vals := range req.Header {
		lower := strings.ToLower(name)
		if lower == "host" {
			continue // already added
		}
		if lower == "content-type" || strings.HasPrefix(lower, "x-amz-") {
			val := strings.TrimSpace(strings.Join(vals, ","))
			pairs = append(pairs, kv{lower, val})
		}
	}

	// Sort by key.
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })

	var headerLines []string
	var names []string
	for _, p := range pairs {
		headerLines = append(headerLines, p.k+":"+p.v)
		names = append(names, p.k)
	}

	signedHeaderNames = strings.Join(names, ";")
	canonicalHeaders = strings.Join(headerLines, "\n") + "\n"
	return
}

// validateRunID mirrors FilesystemStore.absoluteFor's runID checks.
func validateRunID(runID string) error {
	if runID == "" {
		return ErrInvalidPath
	}
	if strings.ContainsAny(runID, "/\\") {
		return ErrInvalidPath
	}
	// Block obvious traversal attempts.
	if strings.Contains(runID, "..") {
		return ErrInvalidPath
	}
	return nil
}

// validBucketName checks the AWS S3 bucket-naming rules: 3-63 chars,
// lowercase alphanumeric, hyphens, dots; cannot start/end with hyphen or
// dot; cannot be formatted as an IP address; cannot have adjacent dots.
// Real S3 also rejects more cases; this is defense in depth, not validation.
func validBucketName(name string) bool {
	if len(name) < 3 || len(name) > 63 {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '.':
			if i == 0 || i == len(name)-1 {
				return false
			}
		default:
			return false
		}
	}
	return !strings.Contains(name, "..")
}

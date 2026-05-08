// HTTP integration tests for the v1.2 datasets endpoints.
//
// These also serve as the acceptance test for D.7 — uploading the same bytes
// under two different names produces ONE physical CAS file and TWO datasets_v2
// rows referencing the same content_hash.
package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/gorevds/litemlflow/internal/api/native"
	"github.com/gorevds/litemlflow/internal/datasets"
	"github.com/gorevds/litemlflow/internal/store"
)

func openDatasetTestServer(t *testing.T) (*httptest.Server, *store.SQLiteStore, datasets.Store, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	st, err := store.OpenSQLite(context.Background(), dbPath, dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	casRoot := filepath.Join(dir, "cas")
	cas, err := datasets.NewFilesystemCAS(casRoot)
	if err != nil {
		t.Fatalf("cas: %v", err)
	}
	mux := http.NewServeMux()
	h := &native.Handler{Store: st, Datasets: cas}
	chi := chiTestRouter(h)
	mux.Handle("/", chi)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_ = slog.Default()
	return srv, st, cas, casRoot
}

// chiTestRouter mounts only the native handler routes for these tests.
func chiTestRouter(h *native.Handler) http.Handler {
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

func uploadDataset(t *testing.T, srvURL, name string, content []byte, meta map[string]any) (*http.Response, []byte) {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	if meta != nil {
		mb, _ := json.Marshal(meta)
		_ = mw.WriteField("meta", string(mb))
	}
	fw, err := mw.CreateFormFile("file", "data.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	mw.Close()

	req, _ := http.NewRequest("POST", srvURL+"/api/v1/datasets/"+name+"/versions", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp, respBody
}

// TestDatasetUploadAndGet — D.4 happy path: upload a small dataset, fetch
// metadata, fetch content; verify hash matches.
func TestDatasetUploadAndGet(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := openDatasetTestServer(t)

	body := []byte("alpha,beta,gamma\n1,2,3\n4,5,6\n")
	resp, raw := uploadDataset(t, srv.URL, "tiny", body, map[string]any{
		"description": "test upload",
		"schema_json": `{"cols":["a","b","c"]}`,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload status %d body=%s", resp.StatusCode, raw)
	}
	var ver struct {
		ID          int64  `json:"id"`
		Version     int64  `json:"version"`
		SizeBytes   int64  `json:"size_bytes"`
		ContentHash string `json:"content_hash"`
		Name        string `json:"name"`
	}
	if err := json.Unmarshal(raw, &ver); err != nil {
		t.Fatalf("parse upload response: %v body=%s", err, raw)
	}
	if ver.Version != 1 || ver.Name != "tiny" || ver.SizeBytes != int64(len(body)) || ver.ContentHash == "" {
		t.Errorf("response unexpected: %+v", ver)
	}

	// Fetch content.
	got, err := http.Get(srv.URL + "/api/v1/datasets/tiny/versions/1/content")
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	gotBody, _ := io.ReadAll(got.Body)
	if !bytes.Equal(gotBody, body) {
		t.Errorf("content mismatch: got %q want %q", gotBody, body)
	}
	if got.Header.Get("X-LiteMLflow-Content-Hash") != ver.ContentHash {
		t.Errorf("content-hash header missing/wrong")
	}
}

// TestDatasetDedup — D.7 acceptance: same bytes under two names → one CAS
// file, two rows with the same content_hash.
func TestDatasetDedup(t *testing.T) {
	t.Parallel()
	srv, _, _, casRoot := openDatasetTestServer(t)

	body := bytes.Repeat([]byte("dedup-payload-"), 1024) // ~14 KiB
	r1, raw1 := uploadDataset(t, srv.URL, "lake-a", body, nil)
	if r1.StatusCode != 200 {
		t.Fatalf("upload A: %d %s", r1.StatusCode, raw1)
	}
	r2, raw2 := uploadDataset(t, srv.URL, "lake-b", body, nil)
	if r2.StatusCode != 200 {
		t.Fatalf("upload B: %d %s", r2.StatusCode, raw2)
	}
	var v1, v2 struct {
		ContentHash string `json:"content_hash"`
		Name        string `json:"name"`
	}
	_ = json.Unmarshal(raw1, &v1)
	_ = json.Unmarshal(raw2, &v2)
	if v1.ContentHash == "" || v1.ContentHash != v2.ContentHash {
		t.Fatalf("hashes should match across different names: %q vs %q", v1.ContentHash, v2.ContentHash)
	}
	if v1.Name == v2.Name {
		t.Fatalf("two names should differ")
	}

	// Walk the CAS dir; expect exactly ONE file (excluding ".part" stragglers).
	count := 0
	_ = filepath.Walk(casRoot, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && !strings.HasSuffix(p, ".part") {
			count++
		}
		return nil
	})
	if count != 1 {
		t.Errorf("ACCEPTANCE: expected exactly 1 physical CAS file after two identical uploads, got %d", count)
	}
}

func TestDatasetVersioning(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := openDatasetTestServer(t)

	// Three uploads of DIFFERENT bytes under the SAME name → versions 1, 2, 3.
	for i, body := range [][]byte{
		[]byte("first version"), []byte("second version"), []byte("third version"),
	} {
		resp, raw := uploadDataset(t, srv.URL, "dset", body, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("v%d: %d %s", i+1, resp.StatusCode, raw)
		}
		var ver struct{ Version int64 }
		_ = json.Unmarshal(raw, &ver)
		if ver.Version != int64(i+1) {
			t.Errorf("expected version %d, got %d", i+1, ver.Version)
		}
	}
	// List versions returns 3 newest-first.
	resp, _ := http.Get(srv.URL + "/api/v1/datasets/dset")
	if resp.StatusCode != 200 {
		t.Fatalf("list versions: %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	var listed struct {
		Versions []struct{ Version int64 } `json:"versions"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&listed)
	if len(listed.Versions) != 3 {
		t.Errorf("expected 3 versions, got %d", len(listed.Versions))
	}
	if listed.Versions[0].Version != 3 || listed.Versions[2].Version != 1 {
		t.Errorf("expected versions desc: %+v", listed.Versions)
	}
}

func TestDatasetLineage(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := openDatasetTestServer(t)

	// parent v1, child v1 referencing parent.
	rp, rawp := uploadDataset(t, srv.URL, "parent", []byte("p-bytes"), nil)
	if rp.StatusCode != 200 {
		t.Fatal(string(rawp))
	}
	var pv struct{ ID int64 }
	_ = json.Unmarshal(rawp, &pv)

	rc, rawc := uploadDataset(t, srv.URL, "child", []byte("c-bytes"), map[string]any{
		"parents": []int64{pv.ID},
	})
	if rc.StatusCode != 200 {
		t.Fatalf("child upload: %d %s", rc.StatusCode, rawc)
	}

	// Child lineage shows parent as ancestor.
	resp, err := http.Get(srv.URL + "/api/v1/datasets/child/versions/1/lineage")
	if err != nil {
		t.Fatalf("get child lineage: %v", err)
	}
	defer resp.Body.Close()
	var lin struct {
		Self      struct{ Name string }
		Ancestors []struct{ ID int64; Name string }
	}
	_ = json.NewDecoder(resp.Body).Decode(&lin)
	if lin.Self.Name != "child" {
		t.Errorf("self.name: %q", lin.Self.Name)
	}
	if len(lin.Ancestors) != 1 || lin.Ancestors[0].Name != "parent" || lin.Ancestors[0].ID != pv.ID {
		t.Errorf("ancestors: %+v", lin.Ancestors)
	}

	// Parent lineage shows child as descendant.
	resp2, err := http.Get(srv.URL + "/api/v1/datasets/parent/versions/1/lineage")
	if err != nil {
		t.Fatalf("get parent lineage: %v", err)
	}
	defer resp2.Body.Close()
	var lin2 struct {
		Descendants []struct{ Name string }
	}
	_ = json.NewDecoder(resp2.Body).Decode(&lin2)
	if len(lin2.Descendants) != 1 || lin2.Descendants[0].Name != "child" {
		t.Errorf("descendants: %+v", lin2.Descendants)
	}
}

func TestDatasetRejectsInvalidParent(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := openDatasetTestServer(t)

	// Upload referencing a non-existent parent ID — must reject.
	resp, raw := uploadDataset(t, srv.URL, "child", []byte("x"), map[string]any{
		"parents": []int64{99999},
	})
	if resp.StatusCode != 400 {
		t.Fatalf("expected 400 for invalid parent, got %d (%s)", resp.StatusCode, raw)
	}
}

func TestDatasetSoftDelete(t *testing.T) {
	t.Parallel()
	srv, _, _, _ := openDatasetTestServer(t)

	resp, _ := uploadDataset(t, srv.URL, "rm-me", []byte("data"), nil)
	if resp.StatusCode != 200 {
		t.Fatal("upload")
	}
	// Soft-delete v1.
	req, _ := http.NewRequest("DELETE", srv.URL+"/api/v1/datasets/rm-me/versions/1", nil)
	dr, _ := http.DefaultClient.Do(req)
	dr.Body.Close()
	if dr.StatusCode != 200 {
		t.Errorf("delete: %d", dr.StatusCode)
	}
	// List shows 0 (latest active is gone).
	lr, err := http.Get(srv.URL + "/api/v1/datasets")
	if err != nil {
		t.Fatalf("list datasets: %v", err)
	}
	defer lr.Body.Close()
	var ds struct {
		Datasets []any
	}
	_ = json.NewDecoder(lr.Body).Decode(&ds)
	if len(ds.Datasets) != 0 {
		t.Errorf("expected 0 active datasets after soft delete, got %d", len(ds.Datasets))
	}
}

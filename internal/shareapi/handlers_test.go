package shareapi

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func gzipBytes(t *testing.T, data string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(data)); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	store := openTestStore(t)
	srv, err := NewServer(store, "https://example.test/r", 0, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func TestCreateAndFetchRoundTrip(t *testing.T) {
	_, ts := newTestServer(t)
	payload := gzipBytes(t, `{"v":1,"hello":"world"}`)

	// POST
	resp, err := http.Post(ts.URL+"/api/r", "application/octet-stream", bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST status = %d, body = %s", resp.StatusCode, body)
	}
	var post postResponse
	if err := json.NewDecoder(resp.Body).Decode(&post); err != nil {
		t.Fatalf("decode post response: %v", err)
	}
	resp.Body.Close()

	if !ValidCode(post.Code) {
		t.Errorf("returned code %q is not valid", post.Code)
	}
	if !strings.HasPrefix(post.URL, "https://example.test/r/") {
		t.Errorf("returned URL %q does not use configured base", post.URL)
	}

	// GET
	resp, err = http.Get(ts.URL + "/api/r/" + post.Code)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET status = %d, body = %s", resp.StatusCode, body)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	resp.Body.Close()

	if !bytes.Equal(got, payload) {
		t.Errorf("payload mismatch: got %d bytes, want %d", len(got), len(payload))
	}
	if ce := resp.Header.Get("Content-Encoding"); ce != "" {
		t.Errorf("Content-Encoding = %q, want empty (browser must not auto-decompress)", ce)
	}
}

func TestCreateRejectsNonGzip(t *testing.T) {
	_, ts := newTestServer(t)
	resp, err := http.Post(ts.URL+"/api/r", "application/octet-stream",
		strings.NewReader(`{"not":"gzip"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestCreateRejectsEmpty(t *testing.T) {
	_, ts := newTestServer(t)
	resp, err := http.Post(ts.URL+"/api/r", "application/octet-stream",
		strings.NewReader(""))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestCreateRejectsTooLarge(t *testing.T) {
	store := openTestStore(t)
	srv, err := NewServer(store, "https://example.test/r", 100, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 2 KB of bytes that start with the gzip magic header — content
	// is arbitrary because the size check fires before gzip validation.
	big := make([]byte, 2048)
	big[0] = 0x1f
	big[1] = 0x8b

	resp, err := http.Post(ts.URL+"/api/r", "application/octet-stream", bytes.NewReader(big))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("status = %d, want 413, body = %s", resp.StatusCode, body)
	}
}

func TestFetchInvalidCode(t *testing.T) {
	_, ts := newTestServer(t)
	cases := []string{"short", "toolongtoolong", "bad!char", ""}
	for _, c := range cases {
		resp, err := http.Get(ts.URL + "/api/r/" + c)
		if err != nil {
			t.Fatalf("GET %q: %v", c, err)
		}
		// Empty path becomes a different route — net/http returns 404
		// for /api/r/ because the pattern requires a path value.
		if c == "" {
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("GET '' status = %d, want 404", resp.StatusCode)
			}
			continue
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET %q status = %d, want 400", c, resp.StatusCode)
		}
	}
}

func TestFetchNotFound(t *testing.T) {
	_, ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/api/r/00000000")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHealthz(t *testing.T) {
	_, ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("GET healthz: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %v, want ok", body["status"])
	}
}

func TestValidatePublicBase(t *testing.T) {
	good := []string{"https://melisai.dev/r", "http://localhost:8080/r"}
	for _, s := range good {
		if err := ValidatePublicBase(s); err != nil {
			t.Errorf("ValidatePublicBase(%q) err = %v, want nil", s, err)
		}
	}
	bad := []string{"", "javascript:alert(1)", "ftp://example.com/", "not a url", "https://"}
	for _, s := range bad {
		if err := ValidatePublicBase(s); err == nil {
			t.Errorf("ValidatePublicBase(%q) err = nil, want error", s)
		}
	}
}

func TestNewServerRejectsBadPublicBase(t *testing.T) {
	store := openTestStore(t)
	if _, err := NewServer(store, "javascript:alert(1)", 0, nil); err == nil {
		t.Error("NewServer accepted javascript: scheme")
	}
}

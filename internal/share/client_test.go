package share

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dmitriimaksimovdevelop/melisai/internal/model"
)

func minimalPayload() *Payload {
	return BuildPayload(&model.Report{
		Metadata: model.Metadata{
			Hostname: "h", KernelVersion: "k", Profile: "p", Version: "v",
		},
		Summary: model.Summary{HealthScore: 50},
	})
}

func TestUploadSuccess(t *testing.T) {
	var receivedBody []byte
	var receivedCT string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedCT = r.Header.Get("Content-Type")
		receivedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(PostResponse{Code: "abc12345", URL: "https://srv/r/abc12345"})
	}))
	defer ts.Close()

	url, err := Upload(context.Background(), minimalPayload(), ts.URL, time.Second)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if url != "https://srv/r/abc12345" {
		t.Errorf("url = %q, want server-provided", url)
	}
	if receivedCT != "application/octet-stream" {
		t.Errorf("Content-Type sent = %q, want application/octet-stream", receivedCT)
	}
	// Verify body is gzip — try to decompress.
	gz, err := gzip.NewReader(strings.NewReader(string(receivedBody)))
	if err != nil {
		t.Errorf("server received non-gzip body: %v", err)
	} else {
		gz.Close()
	}
}

func TestUploadServerError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer ts.Close()

	_, err := Upload(context.Background(), minimalPayload(), ts.URL, time.Second)
	if err == nil {
		t.Fatal("Upload err = nil, want error")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err %q does not mention HTTP status", err.Error())
	}
}

func TestUploadEmptyURLInResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"code":"abc12345"}`))
	}))
	defer ts.Close()

	_, err := Upload(context.Background(), minimalPayload(), ts.URL, time.Second)
	if err == nil {
		t.Fatal("Upload err = nil, want error for missing url")
	}
}

func TestUploadTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer ts.Close()

	start := time.Now()
	_, err := Upload(context.Background(), minimalPayload(), ts.URL, 50*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("Upload err = nil, want timeout error")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("Upload took %s — timeout did not fire promptly", elapsed)
	}
}

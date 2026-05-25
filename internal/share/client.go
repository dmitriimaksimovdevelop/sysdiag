package share

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultAPIURL is the production short-link backend endpoint.
const DefaultAPIURL = "https://melisai.dev/api/r"

// DefaultUploadTimeout is the per-request budget for the POST attempt.
// Keep it short enough that interactive users do not stare at a stalled
// CLI on flaky networks — the fragment fallback kicks in afterwards.
const DefaultUploadTimeout = 10 * time.Second

// PostResponse mirrors the JSON returned by POST /api/r on the
// melisai-share-api backend.
type PostResponse struct {
	Code string `json:"code"`
	URL  string `json:"url"`
}

// Upload POSTs the already-gzipped, base64url-decoded payload to a
// melisai-share-api endpoint and returns the short URL it assigns.
//
// The CLI accepts the encoded form (base64url string) from Encode, so
// this function re-encodes it back to raw gzip bytes before sending —
// the backend stores opaque bytes and the viewer page streams them
// through DecompressionStream.
func Upload(ctx context.Context, p *Payload, apiURL string, timeout time.Duration) (string, error) {
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	if timeout <= 0 {
		timeout = DefaultUploadTimeout
	}

	gz, err := encodeGzip(p)
	if err != nil {
		return "", fmt.Errorf("compress payload: %w", err)
	}

	// Single source of truth for the budget: context.WithTimeout. We do
	// NOT also set http.Client.Timeout — that would race with ctx and
	// cancel body reads independently, producing harder-to-read errors.
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, apiURL, bytes.NewReader(gz))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("User-Agent", "melisai-share/1")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("upload returned %d: %s", resp.StatusCode, bytes.TrimSpace(body))
	}

	var pr PostResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if pr.URL == "" {
		return "", fmt.Errorf("server returned empty url (code=%q)", pr.Code)
	}
	return pr.URL, nil
}

// Package share encodes a melisai report summary into a URL fragment
// for one-click sharing via melisai.dev. The payload is gzip-compressed
// and base64url-encoded — there is no backend; the receiving page decodes
// and renders it entirely client-side.
package share

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/dmitriimaksimovdevelop/melisai/internal/model"
)

// PayloadVersion is the schema version embedded into every payload.
// Bump only on incompatible changes — the receiving page must keep
// rendering older versions or display a clear "unsupported version" message.
const PayloadVersion = 1

// DefaultBaseURL is the public landing page that decodes share payloads.
const DefaultBaseURL = "https://melisai.dev/r"

// Payload is the data carried inside the URL fragment.
//
// JSON field names are intentionally short to keep the encoded URL small.
type Payload struct {
	V       int           `json:"v"`
	Meta    PayloadMeta   `json:"meta"`
	Summary model.Summary `json:"summary"`
}

// PayloadMeta carries minimum identifying context so the receiver can show
// what system the report came from without re-fetching anything.
type PayloadMeta struct {
	Hostname     string `json:"hostname"`
	Kernel       string `json:"kernel"`
	CPUs         int    `json:"cpus"`
	MemoryGB     int    `json:"memory_gb"`
	Profile      string `json:"profile"`
	Duration     string `json:"duration"`
	Timestamp    string `json:"timestamp"`
	ToolVersion  string `json:"tool_version"`
	ContainerEnv string `json:"container_env,omitempty"`
}

// BuildPayload extracts the shareable summary slice from a full report.
func BuildPayload(r *model.Report) *Payload {
	return &Payload{
		V: PayloadVersion,
		Meta: PayloadMeta{
			Hostname:     r.Metadata.Hostname,
			Kernel:       r.Metadata.KernelVersion,
			CPUs:         r.Metadata.CPUs,
			MemoryGB:     r.Metadata.MemoryGB,
			Profile:      r.Metadata.Profile,
			Duration:     r.Metadata.Duration,
			Timestamp:    r.Metadata.Timestamp,
			ToolVersion:  r.Metadata.Version,
			ContainerEnv: r.Metadata.ContainerEnv,
		},
		Summary: r.Summary,
	}
}

// Encode serializes the payload to JSON, gzips it, and base64url-encodes
// the result. The output is safe to drop into a URL fragment without
// further escaping.
func Encode(p *Payload) (string, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("marshal payload: %w", err)
	}

	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return "", fmt.Errorf("init gzip: %w", err)
	}
	if _, err := gz.Write(raw); err != nil {
		return "", fmt.Errorf("gzip write: %w", err)
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("gzip close: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(buf.Bytes()), nil
}

// Decode reverses Encode — primarily used by tests and any future
// `melisai share decode` helper. The browser-side decoder follows the
// same algorithm in JavaScript (atob + DecompressionStream).
func Decode(encoded string) (*Payload, error) {
	encoded = strings.TrimPrefix(encoded, "#")

	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}

	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer gz.Close()

	var p Payload
	if err := json.NewDecoder(gz).Decode(&p); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	return &p, nil
}

// BuildURL constructs the final shareable URL. baseURL may be overridden
// for self-hosted receivers; the encoded payload goes into the fragment
// so it never reaches any server.
//
// The receiving page decodes the fragment with `atob` after translating
// the URL-safe base64 alphabet (`-` → `+`, `_` → `/`) and re-padding,
// then pipes the bytes through DecompressionStream('gzip').
func BuildURL(p *Payload, baseURL string) (string, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid base URL %q: must be http(s) with a host", baseURL)
	}

	encoded, err := Encode(p)
	if err != nil {
		return "", err
	}
	return baseURL + "#" + encoded, nil
}

package shareapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// DefaultMaxBodyBytes caps the size of an uploaded payload at 1 MB.
// melisai's gzip-compressed summary is ~1–10 KB in practice; 1 MB
// leaves a generous safety margin without making bombing easy.
const DefaultMaxBodyBytes = 1 << 20

// retryOnCollision is how many fresh codes we try before giving up on
// an insert. With 62^8 ≈ 2×10^14 slots, three retries is already
// extraordinary luck running out — anything beyond hints at storage
// corruption.
const retryOnCollision = 3

// Server is the HTTP layer.
type Server struct {
	store        *Store
	publicBase   string // e.g. "https://melisai.dev/r" — used to build the URL returned by POST
	maxBodyBytes int64
	log          *slog.Logger
}

// NewServer wires the dependencies. publicBase must be a valid http(s)
// URL with a host — main.go validates with ValidatePublicBase before
// reaching here, but we re-check defensively so library callers can't
// embed bogus URLs.
func NewServer(store *Store, publicBase string, maxBodyBytes int64, log *slog.Logger) (*Server, error) {
	if err := ValidatePublicBase(publicBase); err != nil {
		return nil, err
	}
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		store:        store,
		publicBase:   strings.TrimRight(publicBase, "/"),
		maxBodyBytes: maxBodyBytes,
		log:          log,
	}, nil
}

// Handler returns the HTTP handler covering /healthz and /api/r.
// Mount this directly on a net/http server.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /api/r", s.handleCreate)
	mux.HandleFunc("GET /api/r/{code}", s.handleFetch)
	return mux
}

// postResponse is the JSON body returned from a successful POST /api/r.
type postResponse struct {
	Code string `json:"code"`
	URL  string `json:"url"`
}

// errorResponse is the JSON body for all non-2xx responses.
type errorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{Error: msg})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	count, err := s.store.Count(r.Context())
	if err != nil {
		s.log.Error("healthz: count failed", "err", err)
		writeError(w, http.StatusInternalServerError, "store unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"count":  count,
	})
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	// Cap the body before anything else — http.MaxBytesReader returns a
	// typed error after the limit so the read fails fast on huge uploads.
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBodyBytes)
	defer r.Body.Close()

	payload, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("payload exceeds %d bytes", s.maxBodyBytes))
			return
		}
		writeError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if len(payload) == 0 {
		writeError(w, http.StatusBadRequest, "empty body")
		return
	}
	// Sanity-check the gzip magic so we reject obvious junk upfront.
	// The viewer page assumes gzip; storing anything else is a footgun.
	if len(payload) < 2 || payload[0] != 0x1f || payload[1] != 0x8b {
		writeError(w, http.StatusBadRequest, "body must be gzip-compressed")
		return
	}

	code, err := s.putWithRetry(r.Context(), payload)
	if err != nil {
		s.log.Error("create: store put failed", "err", err)
		writeError(w, http.StatusInternalServerError, "store unavailable")
		return
	}

	resp := postResponse{Code: code, URL: s.publicBase + "/" + code}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleFetch(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if !ValidCode(code) {
		writeError(w, http.StatusBadRequest, "invalid code format")
		return
	}

	payload, err := s.store.Get(r.Context(), code)
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "share code not found")
		return
	}
	if err != nil {
		s.log.Error("fetch: store get failed", "code", code, "err", err)
		writeError(w, http.StatusInternalServerError, "store unavailable")
		return
	}

	// Deliver the raw gzip bytes as opaque payload — we deliberately do
	// NOT set Content-Encoding: gzip, because that would make the browser
	// auto-decompress and prevent the viewer page from streaming the
	// bytes through DecompressionStream itself.
	//
	// Cache-Control: short max-age for CDN-style proxies, but no
	// "immutable" — payloads may be deleted by retention sweep and we
	// don't want stale copies to outlive the underlying row.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(payload)
}

func (s *Server) putWithRetry(ctx context.Context, payload []byte) (string, error) {
	for attempt := 0; attempt < retryOnCollision; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		code, err := GenerateCode()
		if err != nil {
			return "", fmt.Errorf("generate code: %w", err)
		}
		err = s.store.Put(ctx, code, payload)
		if err == nil {
			return code, nil
		}
		if errors.Is(err, ErrCodeExists) {
			s.log.Warn("code collision", "code", code, "attempt", attempt)
			continue
		}
		return "", err
	}
	return "", errors.New("exhausted collision retries")
}

// ValidatePublicBase rejects public base URLs that NewServer cannot
// safely embed in POST responses — anything that is not http(s) with
// a host (e.g. javascript:, data:, malformed).
func ValidatePublicBase(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse public base: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("public base must be http(s) with a host, got %q", raw)
	}
	return nil
}

// Package shareapi implements the storage and HTTP layer for short
// melisai share links. The CLI POSTs gzip-encoded payloads here and
// receives a short code; the static viewer page on melisai.dev fetches
// the bytes back by code.
package shareapi

import (
	"crypto/rand"
	"fmt"
)

// CodeLength is the number of base62 characters in a share code.
// 8 chars over 62 symbols = 62^8 ≈ 2.18 × 10^14 possibilities, more than
// enough headroom for collision-free random allocation at human scale.
const CodeLength = 8

// codeAlphabet is the base62 alphabet used for short codes.
// Order does not affect security — it is rejection-sampled from crypto/rand.
const codeAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// GenerateCode returns a fresh 8-character base62 code drawn from
// crypto/rand using rejection sampling to keep the distribution uniform.
func GenerateCode() (string, error) {
	const alphabetLen = byte(len(codeAlphabet))
	// Largest byte value evenly divisible by 62 — values above this are
	// rejected so the modulo is unbiased.
	const maxValid = byte(256 - (256 % int(alphabetLen)))

	out := make([]byte, CodeLength)
	written := 0
	buf := make([]byte, CodeLength)
	for written < CodeLength {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("read rand: %w", err)
		}
		for _, b := range buf {
			if b >= maxValid {
				continue
			}
			out[written] = codeAlphabet[b%alphabetLen]
			written++
			if written == CodeLength {
				break
			}
		}
	}
	return string(out), nil
}

// ValidCode reports whether s could have been produced by GenerateCode.
// Handlers use this to fail fast on malformed paths before touching
// the database.
func ValidCode(s string) bool {
	if len(s) != CodeLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		default:
			return false
		}
	}
	return true
}

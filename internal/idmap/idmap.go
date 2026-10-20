// Package idmap defines the WORLD ASCII transport encoding for opaque IDs.
//
// Canonical IDs stay exact valid UTF-8 opaque values: no trimming, case folding,
// Unicode normalization, or aliasing. A transport that requires ASCII uses this
// reversible mapping:
//
//   - an ID made only of RFC 3986 unreserved ASCII characters (A-Z, a-z, 0-9,
//     '-', '.', '_', '~') whose first byte is not '~' is unchanged;
//   - every other ID is "~" + base64url(UTF-8 bytes), without padding.
//
// Encoded output always starts with '~', and an ID that starts with '~' is always
// encoded, so the mapping is injective and collision-free.
package idmap

import (
	"encoding/base64"
	"errors"
	"strings"
	"unicode/utf8"
)

// ErrInvalidID is returned for an invalid canonical ID or a noncanonical
// transport representation.
var ErrInvalidID = errors.New("invalid WORLD identifier")

const escape = '~'

// Valid reports whether id is an exact R05-compatible opaque ID: nonempty valid
// UTF-8, no surrounding whitespace, and no control characters.
func Valid(id string) bool {
	if id == "" || !utf8.ValidString(id) || strings.TrimSpace(id) != id {
		return false
	}
	for _, r := range id {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// Encode returns the canonical ASCII transport form of id.
func Encode(id string) (string, error) {
	if !Valid(id) {
		return "", ErrInvalidID
	}
	if isTransportSafe(id) && id[0] != escape {
		return id, nil
	}
	return string(escape) + base64.RawURLEncoding.EncodeToString([]byte(id)), nil
}

// Decode restores the exact canonical ID from its ASCII transport form.
func Decode(transport string) (string, error) {
	if transport == "" {
		return "", ErrInvalidID
	}
	if transport[0] == escape {
		payload := transport[1:]
		if payload == "" {
			return "", ErrInvalidID
		}
		raw, err := base64.RawURLEncoding.DecodeString(payload)
		if err != nil {
			return "", ErrInvalidID
		}
		if base64.RawURLEncoding.EncodeToString(raw) != payload {
			return "", ErrInvalidID
		}
		id := string(raw)
		if !Valid(id) {
			return "", ErrInvalidID
		}
		return id, nil
	}
	if !isTransportSafe(transport) || !Valid(transport) {
		return "", ErrInvalidID
	}
	return transport, nil
}

func isTransportSafe(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '-', '.', '_', '~':
			continue
		}
		return false
	}
	return true
}

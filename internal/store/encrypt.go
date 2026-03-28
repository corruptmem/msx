package store

// Optional AES-256-GCM encryption for token values stored in bbolt.
//
// # Key management
//
// Set MSX_STORE_KEY to a 32-byte value encoded as:
//   - 64 hex characters  (e.g. openssl rand -hex 32)
//   - 44 base64 characters (e.g. openssl rand -base64 32)
//
// When MSX_STORE_KEY is set:
//   - Token bytes written to disk are AES-256-GCM ciphertext prefixed with a
//     12-byte random nonce.  The first byte is a version tag (0x01) so that
//     future algorithms can be distinguished.
//   - Attempting to open a store whose tokens were written without a key will
//     fail with a clear error.
//
// When MSX_STORE_KEY is NOT set:
//   - Raw JSON is stored, identical to prior behaviour.
//   - Attempting to open a store whose tokens were written with a key will
//     fail with a clear error.
//
// # Durability
//
// bbolt's write path is crash-safe (fsync + rename).  Encryption/decryption
// happens in memory before the byte slice reaches bbolt, so crash durability
// is unchanged.  The key itself is never persisted to disk by msx.
//
// # Automation ergonomics
//
// Scripts can pass the key via env var without changing any msx flags.  The
// key is not printed in any log or status output.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	encVersionTag  byte = 0x01
	encNonceSize        = 12 // GCM standard nonce
	encOverhead         = 1 + encNonceSize // version tag + nonce
)

// encryptionKey returns the parsed store key from the environment, or nil if
// none is configured.  Returns an error if the variable is set but malformed.
func encryptionKey() ([]byte, error) {
	raw := os.Getenv("MSX_STORE_KEY")
	if raw == "" {
		return nil, nil
	}
	// Try hex first (64 chars → 32 bytes), then base64.
	if len(raw) == 64 {
		key, err := hex.DecodeString(raw)
		if err == nil && len(key) == 32 {
			return key, nil
		}
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err == nil && len(key) == 32 {
		return key, nil
	}
	key, err = base64.RawStdEncoding.DecodeString(raw)
	if err == nil && len(key) == 32 {
		return key, nil
	}
	return nil, errors.New("MSX_STORE_KEY must be a 32-byte value encoded as 64 hex chars or 44 base64 chars")
}

// sealToken encrypts plaintext (JSON token bytes) using the given key.
// Output format: [0x01][12-byte nonce][ciphertext].
func sealToken(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, encNonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	out := make([]byte, 0, encOverhead+len(plaintext)+gcm.Overhead())
	out = append(out, encVersionTag)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, plaintext, nil)
	return out, nil
}

// openToken decrypts ciphertext produced by sealToken.
func openToken(key, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < encOverhead+1 {
		return nil, errors.New("token ciphertext too short")
	}
	if ciphertext[0] != encVersionTag {
		return nil, fmt.Errorf("unsupported token encryption version 0x%02x", ciphertext[0])
	}
	nonce := ciphertext[1 : 1+encNonceSize]
	payload := ciphertext[1+encNonceSize:]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, payload, nil)
}

// isEncrypted returns true when b looks like it was written by sealToken.
// (First byte is the version tag, not '{' which starts all JSON objects.)
func isEncrypted(b []byte) bool {
	return len(b) > 0 && b[0] == encVersionTag
}

// encodeToken serialises and optionally encrypts a token value.
func encodeToken(key, jsonBytes []byte) ([]byte, error) {
	if key == nil {
		return jsonBytes, nil
	}
	return sealToken(key, jsonBytes)
}

// decodeToken decrypts (if key is set) and returns the raw JSON.
// It enforces consistency: if key is set the data must be encrypted, and vice versa.
func decodeToken(key, raw []byte) ([]byte, error) {
	enc := isEncrypted(raw)
	switch {
	case key == nil && !enc:
		return raw, nil // plain mode, plain data
	case key != nil && enc:
		return openToken(key, raw) // encrypted mode, encrypted data
	case key == nil && enc:
		return nil, errors.New("token is encrypted but MSX_STORE_KEY is not set; export your key or run state-export before re-opening the store")
	default: // key != nil && !enc
		return nil, errors.New("MSX_STORE_KEY is set but token is stored in plain text; if you are migrating, run state-export, then state-import to re-encrypt")
	}
}

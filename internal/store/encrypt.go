package store

// Token encryption / key-management for the bbolt store.
//
// MSX_STORE_KEY controls the mode.  Three values are supported:
//
//	(not set)          – unsafe-plain mode WITH a warning printed to stderr on
//	                     every invocation reminding the user to configure a key.
//	unsafe-plain       – plain text mode, warning suppressed (user has
//	                     explicitly acknowledged this choice).
//	aes-256-gcm:<KEY>  – AES-256-GCM encryption; <KEY> is 32 bytes encoded as
//	                     64 hex characters.
//	keyring            – the encryption key is stored in / retrieved from the
//	                     platform keyring (macOS Keychain, Linux Secret Service,
//	                     Windows DPAPI).  If no key exists yet, one is generated
//	                     and saved automatically.
//
// Wire format for encrypted tokens:
//
//	[0x01][12-byte nonce][AES-256-GCM ciphertext]
//
// Plain tokens are raw JSON (first byte is always '{').  The two formats are
// unambiguous because 0x01 != '{'.
//
// Consistency is enforced: if stored tokens were written in one mode and read
// in another, decodeToken returns a clear error.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/99designs/keyring"
)

const (
	encVersionTag byte = 0x01
	encNonceSize       = 12 // GCM standard nonce
	encOverhead        = 1 + encNonceSize

	keyringService = "msx"
	keyringKey     = "store-key"
)

// storeMode represents how tokens are stored.
type storeMode int

const (
	modePlain     storeMode = iota // no encryption
	modeAES256GCM                  // AES-256-GCM with explicit key
	modeKeyring                    // key from platform keyring
)

// keyConfig holds parsed configuration from MSX_STORE_KEY.
type keyConfig struct {
	mode storeMode
	key  []byte // set for modeAES256GCM and modeKeyring
	warn bool   // true when env var was absent (implicit plain)
}

// parseStoreKeyEnv parses MSX_STORE_KEY and returns the resolved key config.
// It does NOT perform I/O (keyring access happens lazily in resolveKey).
func parseStoreKeyEnv() (keyConfig, error) {
	raw := os.Getenv("MSX_STORE_KEY")

	switch {
	case raw == "":
		return keyConfig{mode: modePlain, warn: true}, nil

	case raw == "unsafe-plain":
		return keyConfig{mode: modePlain, warn: false}, nil

	case raw == "keyring":
		return keyConfig{mode: modeKeyring}, nil

	case strings.HasPrefix(raw, "aes-256-gcm:"):
		hexKey := strings.TrimPrefix(raw, "aes-256-gcm:")
		key, err := hex.DecodeString(hexKey)
		if err != nil || len(key) != 32 {
			return keyConfig{}, errors.New("MSX_STORE_KEY: aes-256-gcm mode requires a 32-byte key encoded as 64 hex characters")
		}
		return keyConfig{mode: modeAES256GCM, key: key}, nil

	default:
		return keyConfig{}, fmt.Errorf("MSX_STORE_KEY: unrecognised value %q (expected: unsafe-plain | aes-256-gcm:<hex-key> | keyring)", raw)
	}
}

// encryptionKey returns the ready-to-use 32-byte AES key, or nil for plain
// mode. It also prints the "unencrypted" warning when appropriate.
// This is the only function that performs I/O (keyring, stderr).
func encryptionKey() ([]byte, error) {
	cfg, err := parseStoreKeyEnv()
	if err != nil {
		return nil, err
	}

	if cfg.warn {
		fmt.Fprintln(os.Stderr, "WARNING: MSX_STORE_KEY is not set. Token store is unencrypted.")
		fmt.Fprintln(os.Stderr, "         Set MSX_STORE_KEY=unsafe-plain to silence this warning, or")
		fmt.Fprintln(os.Stderr, "         set MSX_STORE_KEY=keyring (platform keyring) or")
		fmt.Fprintln(os.Stderr, "         set MSX_STORE_KEY=aes-256-gcm:<64-hex-chars> for encryption.")
	}

	switch cfg.mode {
	case modePlain:
		return nil, nil

	case modeAES256GCM:
		return cfg.key, nil

	case modeKeyring:
		return resolveKeyringKey()

	default:
		return nil, fmt.Errorf("unexpected store mode %d", cfg.mode)
	}
}

// resolveKeyringKey retrieves the encryption key from the platform keyring,
// generating and saving a new one if none exists yet.
func resolveKeyringKey() ([]byte, error) {
	kr, err := keyring.Open(keyring.Config{
		ServiceName: keyringService,
		// Allow all backends; keyring selects the best available one for the
		// current platform.
		AllowedBackends: []keyring.BackendType{
			keyring.KeychainBackend,
			keyring.SecretServiceBackend,
			keyring.KWalletBackend,
			keyring.WinCredBackend,
			keyring.FileBackend,
		},
		// FileBackend fallback path (e.g. headless Linux without Secret Service)
		FileDir: defaultKeyringFileDir(),
	})
	if err != nil {
		return nil, fmt.Errorf("open keyring: %w", err)
	}

	item, err := kr.Get(keyringKey)
	if err == keyring.ErrKeyNotFound || (err != nil && strings.Contains(err.Error(), "not found")) {
		// No key yet — generate one and persist it.
		newKey := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, newKey); err != nil {
			return nil, fmt.Errorf("generate keyring key: %w", err)
		}
		encoded := hex.EncodeToString(newKey)
		if err := kr.Set(keyring.Item{
			Key:         keyringKey,
			Data:        []byte(encoded),
			Label:       "msx store encryption key",
			Description: "AES-256-GCM key for msx token store",
		}); err != nil {
			return nil, fmt.Errorf("save keyring key: %w", err)
		}
		return newKey, nil
	}
	if err != nil {
		return nil, fmt.Errorf("retrieve keyring key: %w", err)
	}

	// Decode the stored hex key.
	key, err := hex.DecodeString(strings.TrimSpace(string(item.Data)))
	if err != nil || len(key) != 32 {
		return nil, errors.New("keyring key is corrupt or wrong length; delete it and re-run to regenerate")
	}
	return key, nil
}

func defaultKeyringFileDir() string {
	if h := os.Getenv("MSX_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return home + "/.config/msx"
}

// sealToken encrypts plaintext using AES-256-GCM.
// Output format: [0x01][12-byte nonce][ciphertext+tag].
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
func isEncrypted(b []byte) bool {
	return len(b) > 0 && b[0] == encVersionTag
}

// encodeToken optionally encrypts token JSON bytes.
func encodeToken(key, jsonBytes []byte) ([]byte, error) {
	if key == nil {
		return jsonBytes, nil
	}
	return sealToken(key, jsonBytes)
}

// decodeToken decrypts (if key is set) and returns the raw JSON.
// Consistency is enforced: mode mismatch returns a clear error.
func decodeToken(key, raw []byte) ([]byte, error) {
	enc := isEncrypted(raw)
	switch {
	case key == nil && !enc:
		return raw, nil
	case key != nil && enc:
		return openToken(key, raw)
	case key == nil && enc:
		return nil, errors.New("token is encrypted but MSX_STORE_KEY is not set (or set to unsafe-plain); export your key or run state-export before re-opening the store")
	default: // key != nil && !enc
		return nil, errors.New("MSX_STORE_KEY is set but token is stored in plain text; if you are migrating, run state-export, then state-import to re-encrypt")
	}
}

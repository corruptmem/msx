package store

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func testKey(t *testing.T) []byte {
	t.Helper()
	k, err := hex.DecodeString("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestSealOpenRoundtrip(t *testing.T) {
	key := testKey(t)
	plain := []byte(`{"access_token":"tok","refresh_token":"ref"}`)
	sealed, err := sealToken(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	if !isEncrypted(sealed) {
		t.Fatal("sealed bytes not tagged as encrypted")
	}
	if bytes.Equal(sealed, plain) {
		t.Fatal("sealed bytes unexpectedly equal to plaintext")
	}
	got, err := openToken(key, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("roundtrip mismatch: got %s want %s", got, plain)
	}
}

func TestSealProducesDistinctNonces(t *testing.T) {
	key := testKey(t)
	plain := []byte(`{"x":1}`)
	a, _ := sealToken(key, plain)
	b, _ := sealToken(key, plain)
	if bytes.Equal(a, b) {
		t.Fatal("two seals of same plaintext produced identical ciphertext (nonce reuse)")
	}
}

func TestDecodeTokenConsistency(t *testing.T) {
	key := testKey(t)
	plain := []byte(`{"access_token":"a","refresh_token":"b"}`)

	// No key, plain data → ok
	out, err := decodeToken(nil, plain)
	if err != nil || !bytes.Equal(out, plain) {
		t.Fatalf("plain/plain failed: %v", err)
	}

	sealed, _ := sealToken(key, plain)

	// Key set, encrypted data → ok
	out, err = decodeToken(key, sealed)
	if err != nil || !bytes.Equal(out, plain) {
		t.Fatalf("enc/enc failed: %v", err)
	}

	// Key not set, encrypted data → error
	_, err = decodeToken(nil, sealed)
	if err == nil {
		t.Fatal("expected error: key not set but data is encrypted")
	}

	// Key set, plain data → error
	_, err = decodeToken(key, plain)
	if err == nil {
		t.Fatal("expected error: key set but data is plain text")
	}
}

// TestParseStoreKeyEnv covers the new multi-mode env var parsing.
func TestParseStoreKeyEnv(t *testing.T) {
	hexKey := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

	t.Run("empty → warn plain", func(t *testing.T) {
		os.Unsetenv("MSX_STORE_KEY")
		cfg, err := parseStoreKeyEnv()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.mode != modePlain {
			t.Fatalf("want modePlain, got %d", cfg.mode)
		}
		if !cfg.warn {
			t.Fatal("want warn=true when env var is absent")
		}
	})

	t.Run("unsafe-plain → no warn", func(t *testing.T) {
		t.Setenv("MSX_STORE_KEY", "unsafe-plain")
		cfg, err := parseStoreKeyEnv()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.mode != modePlain {
			t.Fatalf("want modePlain, got %d", cfg.mode)
		}
		if cfg.warn {
			t.Fatal("want warn=false for explicit unsafe-plain")
		}
	})

	t.Run("aes-256-gcm valid", func(t *testing.T) {
		t.Setenv("MSX_STORE_KEY", "aes-256-gcm:"+hexKey)
		cfg, err := parseStoreKeyEnv()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.mode != modeAES256GCM {
			t.Fatalf("want modeAES256GCM, got %d", cfg.mode)
		}
		if len(cfg.key) != 32 {
			t.Fatalf("want 32-byte key, got %d", len(cfg.key))
		}
	})

	t.Run("aes-256-gcm bad key", func(t *testing.T) {
		t.Setenv("MSX_STORE_KEY", "aes-256-gcm:tooshort")
		_, err := parseStoreKeyEnv()
		if err == nil {
			t.Fatal("expected error for malformed aes-256-gcm key")
		}
	})

	t.Run("keyring", func(t *testing.T) {
		t.Setenv("MSX_STORE_KEY", "keyring")
		cfg, err := parseStoreKeyEnv()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.mode != modeKeyring {
			t.Fatalf("want modeKeyring, got %d", cfg.mode)
		}
	})

	t.Run("unknown value", func(t *testing.T) {
		t.Setenv("MSX_STORE_KEY", "somerandombadvalue")
		_, err := parseStoreKeyEnv()
		if err == nil {
			t.Fatal("expected error for unknown value")
		}
		if !strings.Contains(err.Error(), "unrecognised value") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// TestEncryptionKeyEnvVar exercises the encryptionKey() helper (non-keyring paths only).
func TestEncryptionKeyEnvVar(t *testing.T) {
	t.Run("empty → nil key (warn)", func(t *testing.T) {
		os.Unsetenv("MSX_STORE_KEY")
		key, err := encryptionKey()
		if err != nil || key != nil {
			t.Fatalf("expected nil key, got %v err=%v", key, err)
		}
	})
	t.Run("unsafe-plain → nil key", func(t *testing.T) {
		t.Setenv("MSX_STORE_KEY", "unsafe-plain")
		key, err := encryptionKey()
		if err != nil || key != nil {
			t.Fatalf("expected nil key for unsafe-plain, got %v err=%v", key, err)
		}
	})
	t.Run("aes-256-gcm valid hex", func(t *testing.T) {
		t.Setenv("MSX_STORE_KEY", "aes-256-gcm:0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
		key, err := encryptionKey()
		if err != nil || len(key) != 32 {
			t.Fatalf("expected 32-byte key, got len=%d err=%v", len(key), err)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		t.Setenv("MSX_STORE_KEY", "tooshort")
		_, err := encryptionKey()
		if err == nil {
			t.Fatal("expected error for invalid key")
		}
	})
}

func TestStoreEncryptedRoundtrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MSX_STORE_KEY", "aes-256-gcm:0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")

	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	prof := Profile{Name: "test", Authority: "common", ClientID: "client-1", Scopes: []string{"User.Read"}}
	tok := Token{AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999}
	if err := s.SaveProfileAndToken(prof, tok); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetToken("test")
	if err != nil {
		t.Fatal(err)
	}
	if got.AccessToken != "at" || got.RefreshToken != "rt" {
		t.Fatalf("unexpected token: %+v", got)
	}
}

func TestStoreRejectsMismatchedKey(t *testing.T) {
	dir := t.TempDir()

	// Write without key.
	t.Setenv("MSX_STORE_KEY", "unsafe-plain")
	s1, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	prof := Profile{Name: "p", Authority: "common", ClientID: "c"}
	tok := Token{AccessToken: "a", RefreshToken: "r", ExpiresAt: 1}
	if err := s1.SaveProfileAndToken(prof, tok); err != nil {
		t.Fatal(err)
	}
	s1.Close()

	// Re-open with key → should fail on read.
	t.Setenv("MSX_STORE_KEY", "aes-256-gcm:0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20")
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	_, err = s2.GetToken("p")
	if err == nil {
		t.Fatal("expected error: key set but stored token is plain")
	}
}

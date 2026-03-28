package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	profilesBucket = []byte("profiles")
	tokensBucket   = []byte("tokens")
)

type Profile struct {
	Name         string   `json:"name"`
	Authority    string   `json:"authority"`
	ClientID     string   `json:"client_id"`
	Scopes       []string `json:"scopes"`
	AccountEmail string   `json:"account_email,omitempty"`
	TenantHint   string   `json:"tenant_hint,omitempty"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
}

type Token struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	TokenType    string          `json:"token_type"`
	Scope        string          `json:"scope"`
	ExpiresAt    int64           `json:"expires_at"`
	ObtainedAt   int64           `json:"obtained_at"`
	Raw          json.RawMessage `json:"raw"`
}

type Store struct {
	db *bolt.DB
}

func DefaultDir() string {
	if v := os.Getenv("MSX_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "msx")
}

func Open(dir string) (*Store, error) {
	if dir == "" {
		dir = DefaultDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o700)
	dbPath := filepath.Join(dir, "state.db")
	db, err := bolt.Open(dbPath, 0o600, &bolt.Options{Timeout: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(profilesBucket); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists(tokensBucket); err != nil {
			return err
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	_ = os.Chmod(dbPath, 0o600)
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) SaveProfile(profile Profile) error {
	now := time.Now().Unix()
	if profile.CreatedAt == 0 {
		profile.CreatedAt = now
	}
	profile.UpdatedAt = now
	payload, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(profilesBucket).Put([]byte(profile.Name), payload)
	})
}

func (s *Store) SaveToken(profile string, token Token) error {
	payload, err := json.Marshal(token)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(tokensBucket).Put([]byte(profile), payload)
	})
}

func (s *Store) SaveProfileAndToken(profile Profile, token Token) error {
	now := time.Now().Unix()
	if profile.CreatedAt == 0 {
		profile.CreatedAt = now
	}
	profile.UpdatedAt = now
	pp, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	tp, err := json.Marshal(token)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(profilesBucket).Put([]byte(profile.Name), pp); err != nil {
			return err
		}
		return tx.Bucket(tokensBucket).Put([]byte(profile.Name), tp)
	})
}

func (s *Store) GetProfile(name string) (Profile, error) {
	var profile Profile
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(profilesBucket).Get([]byte(name))
		if raw == nil {
			return os.ErrNotExist
		}
		return json.Unmarshal(raw, &profile)
	})
	return profile, err
}

func (s *Store) GetToken(name string) (Token, error) {
	var token Token
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(tokensBucket).Get([]byte(name))
		if raw == nil {
			return os.ErrNotExist
		}
		return json.Unmarshal(raw, &token)
	})
	return token, err
}

func (s *Store) ListProfiles() ([]Profile, error) {
	profiles := []Profile{}
	err := s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(profilesBucket).ForEach(func(_, v []byte) error {
			var profile Profile
			if err := json.Unmarshal(v, &profile); err != nil {
				return err
			}
			profiles = append(profiles, profile)
			return nil
		})
	})
	return profiles, err
}

func (s *Store) RefreshIfNeeded(name string, skew time.Duration, fn func(Profile, Token) (Token, error)) (Token, error) {
	var out Token
	err := s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(profilesBucket)
		tb := tx.Bucket(tokensBucket)
		pr := pb.Get([]byte(name))
		tr := tb.Get([]byte(name))
		if pr == nil || tr == nil {
			return fmt.Errorf("profile %q is not configured: %w", name, os.ErrNotExist)
		}
		var profile Profile
		var token Token
		if err := json.Unmarshal(pr, &profile); err != nil {
			return err
		}
		if err := json.Unmarshal(tr, &token); err != nil {
			return err
		}
		if time.Unix(token.ExpiresAt, 0).After(time.Now().Add(skew)) {
			out = token
			return nil
		}
		next, err := fn(profile, token)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(next)
		if err != nil {
			return err
		}
		if err := tb.Put([]byte(name), raw); err != nil {
			return err
		}
		out = next
		return nil
	})
	return out, err
}

func RequireTokenPayload(access, refresh string) error {
	if access == "" || refresh == "" {
		return errors.New("token payload missing access_token or refresh_token")
	}
	return nil
}

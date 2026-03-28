package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
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

const (
	stateBackupSchema  = "msx-state-backup"
	stateBackupVersion = 1
)

type StateBackup struct {
	Schema   string               `json:"schema"`
	Version  int                  `json:"version"`
	Profiles []StateBackupProfile `json:"profiles"`
}

type StateBackupProfile struct {
	Profile Profile `json:"profile"`
	Token   Token   `json:"token"`
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

func (s *Store) ExportProfile(name string) (StateBackup, error) {
	return s.exportProfiles([]string{name})
}

func (s *Store) ExportAllProfiles() (StateBackup, error) {
	return s.exportProfiles(nil)
}

func (s *Store) exportProfiles(names []string) (StateBackup, error) {
	backup := StateBackup{
		Schema:  stateBackupSchema,
		Version: stateBackupVersion,
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		pb := tx.Bucket(profilesBucket)
		tb := tx.Bucket(tokensBucket)
		if len(names) == 0 {
			return pb.ForEach(func(_, v []byte) error {
				var profile Profile
				if err := json.Unmarshal(v, &profile); err != nil {
					return err
				}
				tokenRaw := tb.Get([]byte(profile.Name))
				if tokenRaw == nil {
					return fmt.Errorf("profile %q is missing token state: %w", profile.Name, os.ErrNotExist)
				}
				var token Token
				if err := json.Unmarshal(tokenRaw, &token); err != nil {
					return err
				}
				backup.Profiles = append(backup.Profiles, StateBackupProfile{
					Profile: profile,
					Token:   token,
				})
				return nil
			})
		}
		for _, name := range names {
			profile, token, err := getProfileAndToken(tx, name)
			if err != nil {
				return err
			}
			backup.Profiles = append(backup.Profiles, StateBackupProfile{
				Profile: profile,
				Token:   token,
			})
		}
		return nil
	})
	if err != nil {
		return StateBackup{}, err
	}
	sort.Slice(backup.Profiles, func(i, j int) bool {
		return backup.Profiles[i].Profile.Name < backup.Profiles[j].Profile.Name
	})
	return backup, nil
}

func MarshalStateBackup(backup StateBackup) ([]byte, error) {
	if err := validateStateBackup(backup); err != nil {
		return nil, err
	}
	return json.MarshalIndent(backup, "", "  ")
}

func ParseStateBackup(data []byte) (StateBackup, error) {
	var backup StateBackup
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&backup); err != nil {
		return StateBackup{}, fmt.Errorf("decode state backup: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return StateBackup{}, errors.New("decode state backup: trailing data")
	}
	if err := validateStateBackup(backup); err != nil {
		return StateBackup{}, err
	}
	return backup, nil
}

func (s *Store) ImportStateBackup(backup StateBackup, overwrite bool) error {
	if err := validateStateBackup(backup); err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		pb := tx.Bucket(profilesBucket)
		tb := tx.Bucket(tokensBucket)
		type pending struct {
			name    string
			profile []byte
			token   []byte
		}
		items := make([]pending, 0, len(backup.Profiles))
		seen := make(map[string]struct{}, len(backup.Profiles))
		for _, item := range backup.Profiles {
			name := item.Profile.Name
			if _, ok := seen[name]; ok {
				return fmt.Errorf("state backup contains duplicate profile %q", name)
			}
			seen[name] = struct{}{}
			if !overwrite && pb.Get([]byte(name)) != nil {
				return fmt.Errorf("profile %q already exists; rerun with overwrite enabled", name)
			}
			profilePayload, err := json.Marshal(item.Profile)
			if err != nil {
				return err
			}
			tokenPayload, err := json.Marshal(item.Token)
			if err != nil {
				return err
			}
			items = append(items, pending{
				name:    name,
				profile: profilePayload,
				token:   tokenPayload,
			})
		}
		for _, item := range items {
			if err := pb.Put([]byte(item.name), item.profile); err != nil {
				return err
			}
			if err := tb.Put([]byte(item.name), item.token); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) RefreshIfNeeded(name string, skew time.Duration, fn func(Profile, Token) (Token, error)) (Token, error) {
	var out Token
	err := s.db.Update(func(tx *bolt.Tx) error {
		profile, token, err := getProfileAndToken(tx, name)
		if err != nil {
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
		if err := putToken(tx, name, next); err != nil {
			return err
		}
		out = next
		return nil
	})
	return out, err
}

func (s *Store) ForceRefresh(name string, fn func(Profile, Token) (Token, error)) (Token, error) {
	var out Token
	err := s.db.Update(func(tx *bolt.Tx) error {
		profile, token, err := getProfileAndToken(tx, name)
		if err != nil {
			return err
		}
		next, err := fn(profile, token)
		if err != nil {
			return err
		}
		if err := putToken(tx, name, next); err != nil {
			return err
		}
		out = next
		return nil
	})
	return out, err
}

func getProfileAndToken(tx *bolt.Tx, name string) (Profile, Token, error) {
	pb := tx.Bucket(profilesBucket)
	tb := tx.Bucket(tokensBucket)
	pr := pb.Get([]byte(name))
	tr := tb.Get([]byte(name))
	if pr == nil || tr == nil {
		return Profile{}, Token{}, fmt.Errorf("profile %q is not configured: %w", name, os.ErrNotExist)
	}
	var profile Profile
	var token Token
	if err := json.Unmarshal(pr, &profile); err != nil {
		return Profile{}, Token{}, err
	}
	if err := json.Unmarshal(tr, &token); err != nil {
		return Profile{}, Token{}, err
	}
	return profile, token, nil
}

func putToken(tx *bolt.Tx, name string, token Token) error {
	raw, err := json.Marshal(token)
	if err != nil {
		return err
	}
	return tx.Bucket(tokensBucket).Put([]byte(name), raw)
}

func RequireTokenPayload(access, refresh string) error {
	if access == "" || refresh == "" {
		return errors.New("token payload missing access_token or refresh_token")
	}
	return nil
}

func validateStateBackup(backup StateBackup) error {
	if backup.Schema != stateBackupSchema {
		return fmt.Errorf("unsupported state backup schema %q", backup.Schema)
	}
	if backup.Version != stateBackupVersion {
		return fmt.Errorf("unsupported state backup version %d", backup.Version)
	}
	for _, item := range backup.Profiles {
		if item.Profile.Name == "" {
			return errors.New("state backup profile is missing name")
		}
		if err := RequireTokenPayload(item.Token.AccessToken, item.Token.RefreshToken); err != nil {
			return fmt.Errorf("profile %q: %w", item.Profile.Name, err)
		}
		if !json.Valid(item.Token.Raw) {
			return fmt.Errorf("profile %q: token raw payload is not valid JSON", item.Profile.Name)
		}
	}
	return nil
}

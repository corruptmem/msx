package graph

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/corruptmem/msx/internal/auth"
	"github.com/corruptmem/msx/internal/store"
)

const root = "https://graph.microsoft.com/v1.0"

type tokenRefresher interface {
	RefreshIfNeeded(*store.Store, string, time.Duration) (store.Token, error)
	ForceRefresh(*store.Store, string) (store.Token, error)
}

type authRefresher struct{}

func (authRefresher) RefreshIfNeeded(s *store.Store, profile string, skew time.Duration) (store.Token, error) {
	return auth.RefreshIfNeeded(s, profile, skew)
}

func (authRefresher) ForceRefresh(s *store.Store, profile string) (store.Token, error) {
	return auth.ForceRefresh(s, profile)
}

type Client struct {
	Store      *store.Store
	Profile    string
	BaseURL    string
	HTTPClient *http.Client
	Auth       tokenRefresher
}

func (c Client) Request(method, path string, query map[string]string) (map[string]any, error) {
	if c.BaseURL == "" {
		c.BaseURL = root
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if c.Auth == nil {
		c.Auth = authRefresher{}
	}
	return c.request(method, path, query, false)
}

func (c Client) request(method, path string, query map[string]string, forced bool) (map[string]any, error) {
	var (
		token store.Token
		err   error
	)
	if forced {
		token, err = c.Auth.ForceRefresh(c.Store, c.Profile)
	} else {
		token, err = c.Auth.RefreshIfNeeded(c.Store, c.Profile, 5*time.Minute)
	}
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	endpoint := c.BaseURL + path
	if len(query) > 0 {
		values := url.Values{}
		for k, v := range query {
			values.Set(k, v)
		}
		endpoint += "?" + values.Encode()
	}
	req, err := http.NewRequest(method, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "msx/0")
	if _, ok := query["$search"]; ok {
		req.Header.Set("ConsistencyLevel", "eventual")
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && !forced {
		return c.request(method, path, query, true)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s", string(body))
	}
	if len(body) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	err = json.Unmarshal(body, &out)
	return out, err
}

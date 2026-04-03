package graph

import (
	"bytes"
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

var DefaultHTTPClient = func() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

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

func (c Client) RequestWithBody(method, path string, query map[string]string, body []byte) (map[string]any, error) {
	if c.BaseURL == "" {
		c.BaseURL = root
	}
	if c.HTTPClient == nil {
		c.HTTPClient = DefaultHTTPClient()
	}
	if c.Auth == nil {
		c.Auth = authRefresher{}
	}
	endpoint, err := c.buildEndpoint(path, query)
	if err != nil {
		return nil, err
	}
	return c.requestURLWithBody(method, endpoint, body, false)
}

func (c Client) requestURLWithBody(method, endpoint string, body []byte, forced bool) (map[string]any, error) {
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
	req, err := http.NewRequest(method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "msx/0")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized && !forced {
		return c.requestURLWithBody(method, endpoint, body, true)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s", string(respBody))
	}
	if len(respBody) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	err = json.Unmarshal(respBody, &out)
	return out, err
}

func (c Client) Request(method, path string, query map[string]string) (map[string]any, error) {
	if c.BaseURL == "" {
		c.BaseURL = root
	}
	if c.HTTPClient == nil {
		c.HTTPClient = DefaultHTTPClient()
	}
	if c.Auth == nil {
		c.Auth = authRefresher{}
	}
	endpoint, err := c.buildEndpoint(path, query)
	if err != nil {
		return nil, err
	}
	return c.requestURL(method, endpoint, queryHasSearch(query), false)
}

func (c Client) RequestURL(method, endpoint string) (map[string]any, error) {
	if c.BaseURL == "" {
		c.BaseURL = root
	}
	if c.HTTPClient == nil {
		c.HTTPClient = DefaultHTTPClient()
	}
	if c.Auth == nil {
		c.Auth = authRefresher{}
	}
	endpoint = c.rewriteEndpoint(endpoint)
	return c.requestURL(method, endpoint, queryContainsSearch(endpoint), false)
}

func (c Client) requestURL(method, endpoint string, useSearchHeader bool, forced bool) (map[string]any, error) {
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
	req, err := http.NewRequest(method, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "msx/0")
	if useSearchHeader {
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
		return c.requestURL(method, endpoint, useSearchHeader, true)
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

func (c Client) buildEndpoint(path string, query map[string]string) (string, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	endpoint := c.BaseURL + path
	if len(query) == 0 {
		return endpoint, nil
	}
	values := url.Values{}
	for k, v := range query {
		values.Set(k, v)
	}
	return endpoint + "?" + values.Encode(), nil
}

func (c Client) rewriteEndpoint(endpoint string) string {
	if c.BaseURL == "" || c.BaseURL == root {
		return endpoint
	}
	target, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return endpoint
	}
	if !strings.EqualFold(target.Host, "graph.microsoft.com") {
		return endpoint
	}
	target.Scheme = base.Scheme
	target.Host = base.Host
	prefix := strings.TrimRight(base.Path, "/")
	if prefix != "" && !strings.HasPrefix(target.Path, prefix+"/") && target.Path != prefix {
		target.Path = prefix + target.Path
	}
	return target.String()
}

func queryHasSearch(query map[string]string) bool {
	_, ok := query["$search"]
	return ok
}

func queryContainsSearch(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	_, ok := u.Query()["$search"]
	return ok
}

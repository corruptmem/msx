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

type Client struct {
	Store   *store.Store
	Profile string
}

func (c Client) Request(method, path string, query map[string]string) (map[string]any, error) {
	token, err := auth.RefreshIfNeeded(c.Store, c.Profile, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	endpoint := root + path
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
	if _, ok := query["$search"]; ok {
		req.Header.Set("ConsistencyLevel", "eventual")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
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

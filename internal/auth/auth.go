package auth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/corruptmem/msx/internal/store"
)

var DefaultScopes = []string{
	"openid",
	"profile",
	"offline_access",
	"User.Read",
	"Mail.ReadWrite",
	"Calendars.ReadWrite",
	"Files.ReadWrite",
}

type DeviceCodeFlow struct {
	UserCode        string `json:"user_code"`
	DeviceCode      string `json:"device_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Message         string `json:"message"`
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
}

type opItem struct {
	Fields []struct {
		Label string `json:"label"`
		Value string `json:"value"`
	} `json:"fields"`
}

func BeginDeviceLogin(clientID, authority string, scopes []string) (DeviceCodeFlow, error) {
	values := url.Values{}
	values.Set("client_id", clientID)
	values.Set("scope", strings.Join(scopes, " "))
	body, err := formPost(fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/devicecode", authority), values)
	if err != nil {
		return DeviceCodeFlow{}, err
	}
	var flow DeviceCodeFlow
	err = json.Unmarshal(body, &flow)
	return flow, err
}

func FinishDeviceLogin(clientID, authority string, flow DeviceCodeFlow) (store.Token, error) {
	deadline := time.Now().Add(time.Duration(flow.ExpiresIn) * time.Second)
	interval := time.Duration(max(flow.Interval, 1)) * time.Second
	for time.Now().Before(deadline) {
		values := url.Values{}
		values.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		values.Set("client_id", clientID)
		values.Set("device_code", flow.DeviceCode)
		body, err := formPost(fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", authority), values)
		if err == nil {
			return tokenFromJSON(body)
		}
		msg := err.Error()
		if strings.Contains(msg, `"authorization_pending"`) {
			time.Sleep(interval)
			continue
		}
		if strings.Contains(msg, `"slow_down"`) {
			interval += 5 * time.Second
			time.Sleep(interval)
			continue
		}
		return store.Token{}, err
	}
	return store.Token{}, fmt.Errorf("device code flow timed out")
}

func ImportFrom1Password(s *store.Store, profileName, accountItem, appItem, vault string, scopes []string) error {
	if len(scopes) == 0 {
		scopes = DefaultScopes
	}
	app, err := getOPItem(appItem, vault)
	if err != nil {
		return err
	}
	account, err := getOPItem(accountItem, vault)
	if err != nil {
		return err
	}
	clientID := field(app, "client_id")
	tenant := field(app, "tenant_id")
	if tenant == "" {
		tenant = "common"
	}
	authority := field(account, "authority")
	if authority == "" {
		authority = tenant
	}
	refresh := field(account, "refresh_token")
	email := field(account, "account_email")
	if clientID == "" || refresh == "" {
		return fmt.Errorf("1Password items are missing client_id or refresh_token")
	}
	token, err := refreshWithToken(clientID, authority, refresh, scopes)
	if err != nil {
		return err
	}
	profile := store.Profile{
		Name:         profileName,
		Authority:    authority,
		ClientID:     clientID,
		Scopes:       scopes,
		AccountEmail: email,
		TenantHint:   tenant,
	}
	return s.SaveProfileAndToken(profile, token)
}

func RefreshIfNeeded(s *store.Store, profile string, skew time.Duration) (store.Token, error) {
	return s.RefreshIfNeeded(profile, skew, func(p store.Profile, t store.Token) (store.Token, error) {
		return refreshWithToken(p.ClientID, p.Authority, t.RefreshToken, p.Scopes)
	})
}

func refreshWithToken(clientID, authority, refresh string, scopes []string) (store.Token, error) {
	values := url.Values{}
	values.Set("client_id", clientID)
	values.Set("grant_type", "refresh_token")
	values.Set("refresh_token", refresh)
	values.Set("scope", strings.Join(scopes, " "))
	body, err := formPost(fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", authority), values)
	if err != nil {
		return store.Token{}, err
	}
	return tokenFromJSON(body)
}

func tokenFromJSON(body []byte) (store.Token, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return store.Token{}, err
	}
	access, _ := payload["access_token"].(string)
	refresh, _ := payload["refresh_token"].(string)
	if err := store.RequireTokenPayload(access, refresh); err != nil {
		return store.Token{}, err
	}
	tokenType, _ := payload["token_type"].(string)
	scope, _ := payload["scope"].(string)
	expiresIn, _ := payload["expires_in"].(float64)
	raw, err := json.Marshal(payload)
	if err != nil {
		return store.Token{}, err
	}
	now := time.Now().Unix()
	return store.Token{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    orDefault(tokenType, "Bearer"),
		Scope:        scope,
		ExpiresAt:    now + int64(expiresIn),
		ObtainedAt:   now,
		Raw:          raw,
	}, nil
}

func formPost(endpoint string, values url.Values) ([]byte, error) {
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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
	return body, nil
}

func getOPItem(title, vault string) (opItem, error) {
	cmd := exec.Command("op", "item", "get", title, "--vault", vault, "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return opItem{}, err
	}
	var item opItem
	err = json.Unmarshal(out, &item)
	return item, err
}

func field(item opItem, label string) string {
	for _, f := range item.Fields {
		if f.Label == label {
			return f.Value
		}
	}
	return ""
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

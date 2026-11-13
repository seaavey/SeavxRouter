package kiro

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"seavxrouter/internal/auth"
)

// AWS SSO OIDC scopes required by the CodeWhisperer/Kiro runtime.
var kiroScopes = []string{
	"codewhisperer:completions",
	"codewhisperer:analysis",
	"codewhisperer:conversations",
	"codewhisperer:transformations",
	"codewhisperer:taskassist",
}

type kiroTokenResponse struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	ProfileARN   string
	ExpiresIn    int
	Error        string
}

// Login runs the AWS SSO OIDC device-authorization flow (Builder ID) and
// stores the resulting account.
func (e *Engine) Login(ctx context.Context) error {
	region := e.Region
	if region == "" {
		region = kiroDefaultRegion
	}
	startURL := e.StartURL
	if startURL == "" {
		startURL = kiroStartURL
	}
	base := e.oidcBase(region)

	regBody, _ := json.Marshal(map[string]any{
		"clientName": "Amazon Q Developer for command line",
		"clientType": "public",
		"scopes":     kiroScopes,
		"grantTypes": []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"},
	})
	status, body, err := e.postBytes(ctx, base+"/client/register", "application/json", regBody)
	if err != nil {
		return err
	}
	var reg struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &reg) != nil || reg.ClientID == "" {
		return fmt.Errorf("kiro client registration failed: %s", snippet(body))
	}

	devBody, _ := json.Marshal(map[string]string{
		"clientId":     reg.ClientID,
		"clientSecret": reg.ClientSecret,
		"startUrl":     startURL,
	})
	status, body, err = e.postBytes(ctx, base+"/device_authorization", "application/json", devBody)
	if err != nil {
		return err
	}
	var dev struct {
		DeviceCode              string `json:"deviceCode"`
		UserCode                string `json:"userCode"`
		VerificationURI         string `json:"verificationUri"`
		VerificationURIComplete string `json:"verificationUriComplete"`
		ExpiresIn               int    `json:"expiresIn"`
		Interval                int    `json:"interval"`
	}
	if status != http.StatusOK || json.Unmarshal(body, &dev) != nil || dev.DeviceCode == "" {
		return fmt.Errorf("kiro device authorization failed: %s", snippet(body))
	}

	openURL := dev.VerificationURI
	if dev.VerificationURIComplete != "" {
		openURL = dev.VerificationURIComplete
	}
	fmt.Printf("To sign in to Kiro with your AWS Builder ID, open:\n\n%s\n\nand confirm this code: %s\n\n", dev.VerificationURI, dev.UserCode)
	auth.OpenBrowser(openURL)

	expires := time.Duration(dev.ExpiresIn) * time.Second
	if expires <= 0 {
		expires = 10 * time.Minute
	}
	pollCtx, cancel := context.WithTimeout(ctx, expires)
	defer cancel()

	interval := e.PollInterval
	if interval <= 0 {
		interval = time.Duration(dev.Interval) * time.Second
		if interval < 5*time.Second {
			interval = 5 * time.Second
		}
	}
	tokenBody, _ := json.Marshal(map[string]string{
		"clientId":     reg.ClientID,
		"clientSecret": reg.ClientSecret,
		"deviceCode":   dev.DeviceCode,
		"grantType":    "urn:ietf:params:oauth:grant-type:device_code",
	})
	for {
		select {
		case <-pollCtx.Done():
			return fmt.Errorf("kiro login timed out")
		case <-time.After(interval):
		}
		status, body, err := e.postBytes(pollCtx, base+"/token", "application/json", tokenBody)
		if err != nil {
			return err
		}
		tok, _ := decodeKiroToken(body)
		if status == http.StatusOK && tok != nil && tok.AccessToken != "" {
			return e.saveLogin(region, reg.ClientID, reg.ClientSecret, tok)
		}
		if tok != nil {
			switch tok.Error {
			case "authorization_pending":
				continue
			case "slow_down":
				interval += 5 * time.Second
				continue
			case "":
				return fmt.Errorf("kiro token endpoint: %s", snippet(body))
			default:
				return fmt.Errorf("kiro device authorization failed: %s", tok.Error)
			}
		}
		return fmt.Errorf("kiro token endpoint: %s", snippet(body))
	}
}

func (e *Engine) saveLogin(region, clientID, clientSecret string, tok *kiroTokenResponse) error {
	if tok.RefreshToken == "" {
		return fmt.Errorf("kiro did not return a refresh_token; try logging in again")
	}
	expires := tok.ExpiresIn
	if expires <= 0 {
		expires = 3600
	}
	acct := &auth.Auth{
		Provider:     "kiro",
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       time.Now().Add(time.Duration(expires)*time.Second - time.Minute),
		Region:       region,
		ClientID:     clientID,
		ClientSecret: clientSecret,
		ProfileARN:   tok.ProfileARN,
	}
	acct.Email, acct.AccountID = kiroTokenClaims(tok.IDToken, tok.AccessToken)
	if acct.AccountID == "" {
		acct.AccountID = acct.Email
	}

	existing, _ := e.Store.Load()
	own := make([]*auth.Auth, 0, len(existing)+1)
	for _, a := range existing {
		if a.Provider == "kiro" {
			own = append(own, a)
		}
	}
	merged := false
	for i, a := range own {
		if acct.Email != "" && strings.EqualFold(a.Email, acct.Email) {
			own[i] = acct
			merged = true
			break
		}
	}
	if !merged {
		own = append(own, acct)
	}
	e.mu.Lock()
	e.auths = own
	e.mu.Unlock()
	if err := e.Store.Save(own); err != nil {
		return err
	}
	name := acct.Email
	if name == "" {
		name = region
	}
	fmt.Printf("Logged in as %s (region: %s)\n%d kiro account(s) stored in %s\n", name, region, len(own), e.Store.Path)
	return nil
}

// token returns a usable access token, refreshing it shortly before expiry.
func (e *Engine) token(ctx context.Context, a *auth.Auth) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if a.AccessToken != "" && a.Expiry.After(time.Now().Add(5*time.Minute)) {
		return a.AccessToken, nil
	}
	if a.RefreshToken == "" {
		return "", fmt.Errorf("kiro account has no refresh_token; run: seavxrouter login kiro")
	}
	if err := e.refreshAccount(ctx, a); err != nil {
		return "", err
	}
	if e.Store.Path != "" {
		if err := e.Store.Save(e.auths); err != nil {
			slog.Warn("could not persist refreshed kiro token", "err", err)
		}
	}
	return a.AccessToken, nil
}

func (e *Engine) refreshAccount(ctx context.Context, a *auth.Auth) error {
	region := a.Region
	if region == "" {
		region = kiroDefaultRegion
	}
	var tok *kiroTokenResponse
	if a.ClientID != "" && a.ClientSecret != "" {
		payload, _ := json.Marshal(map[string]string{
			"grantType":    "refresh_token",
			"clientId":     a.ClientID,
			"clientSecret": a.ClientSecret,
			"refreshToken": a.RefreshToken,
		})
		status, body, err := e.postBytes(ctx, e.oidcBase(region)+"/token", "application/json", payload)
		if err != nil {
			return err
		}
		if status == http.StatusBadRequest || status == http.StatusUnsupportedMediaType {
			form := url.Values{
				"grant_type":    {"refresh_token"},
				"client_id":     {a.ClientID},
				"client_secret": {a.ClientSecret},
				"refresh_token": {a.RefreshToken},
			}
			status, body, err = e.postBytes(ctx, e.oidcBase(region)+"/token", "application/x-www-form-urlencoded", []byte(form.Encode()))
			if err != nil {
				return err
			}
		}
		if status != http.StatusOK {
			return fmt.Errorf("kiro token refresh: %d: %s", status, snippet(body))
		}
		tok, err = decodeKiroToken(body)
		if err != nil {
			return err
		}
	} else {
		payload, _ := json.Marshal(map[string]string{"refreshToken": a.RefreshToken})
		status, body, err := e.postBytes(ctx, e.authBase(region)+"/refreshToken", "application/json", payload)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("kiro desktop token refresh: %d: %s", status, snippet(body))
		}
		tok, err = decodeKiroToken(body)
		if err != nil {
			return err
		}
	}
	if tok.AccessToken == "" {
		return fmt.Errorf("kiro token refresh returned no access token")
	}
	a.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		a.RefreshToken = tok.RefreshToken
	}
	if tok.ProfileARN != "" {
		a.ProfileARN = tok.ProfileARN
	}
	expires := tok.ExpiresIn
	if expires <= 0 {
		expires = 3600
	}
	a.Expiry = time.Now().Add(time.Duration(expires)*time.Second - time.Minute)
	return nil
}

func decodeKiroToken(body []byte) (*kiroTokenResponse, error) {
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("kiro token response: %w", err)
	}
	pick := func(keys ...string) string {
		for _, key := range keys {
			if s, ok := m[key].(string); ok && s != "" {
				return s
			}
		}
		return ""
	}
	num := func(keys ...string) int {
		for _, key := range keys {
			if f, ok := m[key].(float64); ok {
				return int(f)
			}
		}
		return 0
	}
	return &kiroTokenResponse{
		AccessToken:  pick("accessToken", "access_token"),
		RefreshToken: pick("refreshToken", "refresh_token"),
		IDToken:      pick("idToken", "id_token"),
		ProfileARN:   pick("profileArn", "profile_arn"),
		ExpiresIn:    num("expiresIn", "expires_in"),
		Error:        pick("error"),
	}, nil
}

// kiroTokenClaims extracts a display email and the stable subject from JWTs,
// when present.
func kiroTokenClaims(tokens ...string) (email, sub string) {
	for _, token := range tokens {
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			continue
		}
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		var claims struct {
			Email             string `json:"email"`
			PreferredUsername string `json:"preferred_username"`
			Username          string `json:"username"`
			Sub               string `json:"sub"`
		}
		if json.Unmarshal(payload, &claims) != nil {
			continue
		}
		for _, v := range []string{claims.Email, claims.PreferredUsername, claims.Username} {
			if v != "" {
				email = v
				break
			}
		}
		sub = claims.Sub
		if email != "" || sub != "" {
			return email, sub
		}
	}
	return "", ""
}

func (e *Engine) oidcBase(region string) string {
	if e.OIDCBase != "" {
		return strings.TrimRight(e.OIDCBase, "/")
	}
	return fmt.Sprintf(kiroOIDCHostTemplate, region)
}

func (e *Engine) authBase(region string) string {
	if e.AuthBase != "" {
		return strings.TrimRight(e.AuthBase, "/")
	}
	return fmt.Sprintf(kiroDesktopHostTemplate, region)
}

func (e *Engine) postBytes(ctx context.Context, endpoint, contentType string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "KiroIDE-0.7.45-"+kiroFingerprint())
	resp, err := e.client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, data, nil
}

func snippet(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

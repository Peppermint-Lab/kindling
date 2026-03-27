package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	gitHubAuthorizeURL   = "https://github.com/login/oauth/authorize"
	gitHubAccessTokenURL = "https://github.com/login/oauth/access_token"
	gitHubAPIBaseURL     = "https://api.github.com"
)

type GitHubConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
}

type gitHubTokenResponse struct {
	AccessToken      string `json:"access_token"`
	Scope            string `json:"scope"`
	TokenType        string `json:"token_type"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	ErrorURI         string `json:"error_uri"`
}

type gitHubUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
	HTMLURL   string `json:"html_url"`
}

type gitHubEmail struct {
	Email      string `json:"email"`
	Primary    bool   `json:"primary"`
	Verified   bool   `json:"verified"`
	Visibility string `json:"visibility"`
}

type gitHubOrg struct {
	Login string `json:"login"`
}

func GitHubAuthorizeURL(cfg GitHubConfig, state, codeChallenge string) string {
	q := url.Values{}
	q.Set("client_id", strings.TrimSpace(cfg.ClientID))
	q.Set("redirect_uri", strings.TrimSpace(cfg.RedirectURL))
	q.Set("scope", strings.Join(cfg.Scopes, " "))
	q.Set("state", state)
	q.Set("allow_signup", "false")
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	return gitHubAuthorizeURL + "?" + q.Encode()
}

func ExchangeGitHubCode(ctx context.Context, client *http.Client, cfg GitHubConfig, code, verifier string) (string, error) {
	values := url.Values{}
	values.Set("client_id", strings.TrimSpace(cfg.ClientID))
	values.Set("client_secret", strings.TrimSpace(cfg.ClientSecret))
	values.Set("code", strings.TrimSpace(code))
	values.Set("redirect_uri", strings.TrimSpace(cfg.RedirectURL))
	values.Set("code_verifier", strings.TrimSpace(verifier))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gitHubAccessTokenURL, strings.NewReader(values.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	res, err := httpClient(client).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return "", err
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github token exchange failed: %s", strings.TrimSpace(string(body)))
	}

	var payload gitHubTokenResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	if payload.Error != "" {
		msg := payload.ErrorDescription
		if strings.TrimSpace(msg) == "" {
			msg = payload.Error
		}
		return "", fmt.Errorf("github token exchange failed: %s", msg)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return "", fmt.Errorf("github token exchange failed: missing access token")
	}
	return payload.AccessToken, nil
}

func GitHubIdentity(ctx context.Context, client *http.Client, accessToken string) (Identity, []string, error) {
	client = httpClient(client)
	user, err := gitHubGET[gitHubUser](ctx, client, accessToken, gitHubAPIBaseURL+"/user")
	if err != nil {
		return Identity{}, nil, err
	}
	emails, err := gitHubGET[[]gitHubEmail](ctx, client, accessToken, gitHubAPIBaseURL+"/user/emails?per_page=100")
	if err != nil {
		return Identity{}, nil, err
	}
	orgs, err := gitHubListOrgs(ctx, client, accessToken)
	if err != nil {
		return Identity{}, nil, err
	}

	email := strings.TrimSpace(user.Email)
	if email == "" {
		email = gitHubBestEmail(emails)
	}
	displayName := strings.TrimSpace(user.Name)
	if displayName == "" {
		displayName = strings.TrimSpace(user.Login)
	}
	orgLogins := make([]string, 0, len(orgs))
	for _, org := range orgs {
		login := strings.TrimSpace(org.Login)
		if login != "" {
			orgLogins = append(orgLogins, login)
		}
	}

	return Identity{
		Subject:     strconv.FormatInt(user.ID, 10),
		Email:       email,
		Login:       strings.TrimSpace(user.Login),
		DisplayName: displayName,
		Claims: map[string]any{
			"id":            user.ID,
			"login":         strings.TrimSpace(user.Login),
			"name":          displayName,
			"email":         email,
			"avatar_url":    strings.TrimSpace(user.AvatarURL),
			"html_url":      strings.TrimSpace(user.HTMLURL),
			"organizations": orgLogins,
		},
	}, orgLogins, nil
}

func gitHubBestEmail(emails []gitHubEmail) string {
	for _, candidate := range emails {
		if candidate.Primary && candidate.Verified && strings.TrimSpace(candidate.Email) != "" {
			return strings.TrimSpace(candidate.Email)
		}
	}
	for _, candidate := range emails {
		if candidate.Verified && strings.TrimSpace(candidate.Email) != "" {
			return strings.TrimSpace(candidate.Email)
		}
	}
	return ""
}

func gitHubListOrgs(ctx context.Context, client *http.Client, accessToken string) ([]gitHubOrg, error) {
	out := make([]gitHubOrg, 0, 8)
	for page := 1; page <= 20; page++ {
		items, err := gitHubGET[[]gitHubOrg](ctx, client, accessToken, fmt.Sprintf("%s/user/orgs?per_page=100&page=%d", gitHubAPIBaseURL, page))
		if err != nil {
			return nil, err
		}
		if len(items) == 0 {
			break
		}
		out = append(out, items...)
		if len(items) < 100 {
			break
		}
	}
	return out, nil
}

func gitHubGET[T any](ctx context.Context, client *http.Client, accessToken, rawURL string) (T, error) {
	var zero T
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(accessToken))
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	res, err := client.Do(req)
	if err != nil {
		return zero, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return zero, err
	}
	if res.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("github api request failed: %s", strings.TrimSpace(string(body)))
	}
	var payload T
	if err := json.Unmarshal(body, &payload); err != nil {
		return zero, err
	}
	return payload, nil
}

func httpClient(client *http.Client) *http.Client {
	if client != nil {
		return client
	}
	return http.DefaultClient
}

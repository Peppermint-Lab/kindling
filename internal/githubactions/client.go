package githubactions

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultAPIBaseURL = "https://api.github.com"

type Client interface {
	ExchangeInstallationToken(ctx context.Context, integration Integration) (string, error)
	GenerateJITConfig(ctx context.Context, req JITConfigRequest) (JITConfig, error)
	DeleteRunner(ctx context.Context, req DeleteRunnerRequest) error
}

type HTTPClient struct {
	baseURL string
	client  *http.Client
}

type JITConfigRequest struct {
	Integration Integration
	RunnerName  string
	Labels      []string
}

type JITConfig struct {
	EncodedJITConfig string
	RunnerID         int64
}

type DeleteRunnerRequest struct {
	Integration Integration
	RunnerID    int64
}

func NewHTTPClient(client *http.Client, baseURL string) *HTTPClient {
	if client == nil {
		client = http.DefaultClient
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = defaultAPIBaseURL
	}
	return &HTTPClient{client: client, baseURL: baseURL}
}

func (c *HTTPClient) ExchangeInstallationToken(ctx context.Context, integration Integration) (string, error) {
	appJWT, err := signAppJWT(integration.Metadata.AppID, integration.Credentials.AppPrivateKeyPEM, time.Now())
	if err != nil {
		return "", err
	}

	body, err := json.Marshal(map[string]any{})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/app/installations/%d/access_tokens", c.baseURL, integration.Metadata.InstallationID), bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create installation token request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+appJWT)
	req.Header.Set("User-Agent", "kindling")

	var resp struct {
		Token string `json:"token"`
	}
	if err := c.doJSON(req, http.StatusCreated, &resp); err != nil {
		return "", err
	}
	token := strings.TrimSpace(resp.Token)
	if token == "" {
		return "", fmt.Errorf("GitHub App installation token response was empty")
	}
	return token, nil
}

func (c *HTTPClient) GenerateJITConfig(ctx context.Context, req JITConfigRequest) (JITConfig, error) {
	token, err := c.ExchangeInstallationToken(ctx, req.Integration)
	if err != nil {
		return JITConfig{}, err
	}

	payload, err := json.Marshal(map[string]any{
		"name":            strings.TrimSpace(req.RunnerName),
		"runner_group_id": req.Integration.Metadata.RunnerGroupID,
		"labels":          normalizeLabels(req.Labels),
		"work_folder":     "_work",
	})
	if err != nil {
		return JITConfig{}, fmt.Errorf("marshal jit config request: %w", err)
	}

	url := fmt.Sprintf("%s/orgs/%s/actions/runners/generate-jitconfig", c.baseURL, req.Integration.Metadata.OrgLogin)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return JITConfig{}, fmt.Errorf("create jit config request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", "kindling")

	var resp struct {
		EncodedJITConfig string `json:"encoded_jit_config"`
		Runner           struct {
			ID int64 `json:"id"`
		} `json:"runner"`
	}
	if err := c.doJSON(httpReq, http.StatusCreated, &resp); err != nil {
		return JITConfig{}, err
	}
	if strings.TrimSpace(resp.EncodedJITConfig) == "" {
		return JITConfig{}, fmt.Errorf("GitHub JIT config response was empty")
	}
	return JITConfig{
		EncodedJITConfig: strings.TrimSpace(resp.EncodedJITConfig),
		RunnerID:         resp.Runner.ID,
	}, nil
}

func (c *HTTPClient) DeleteRunner(ctx context.Context, req DeleteRunnerRequest) error {
	if req.RunnerID <= 0 {
		return nil
	}
	token, err := c.ExchangeInstallationToken(ctx, req.Integration)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/orgs/%s/actions/runners/%d", c.baseURL, req.Integration.Metadata.OrgLogin, req.RunnerID)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create delete runner request: %w", err)
	}
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", "kindling")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("delete GitHub runner: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("delete GitHub runner: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *HTTPClient) doJSON(req *http.Request, wantStatus int, out any) error {
	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("github actions api request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("github actions api %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode GitHub Actions response: %w", err)
	}
	return nil
}

func signAppJWT(appID int64, privateKeyPEM string, now time.Time) (string, error) {
	if appID <= 0 {
		return "", fmt.Errorf("GitHub Actions app_id must be set")
	}
	key, err := parsePEMPrivateKey(privateKeyPEM)
	if err != nil {
		return "", err
	}
	header, err := json.Marshal(map[string]string{
		"alg": "RS256",
		"typ": "JWT",
	})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-30 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": appID,
	})
	if err != nil {
		return "", err
	}
	unsigned := jwtSegment(header) + "." + jwtSegment(claims)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign GitHub App JWT: %w", err)
	}
	return unsigned + "." + jwtSegment(sig), nil
}

func parsePEMPrivateKey(raw string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(raw)))
	if block == nil {
		return nil, fmt.Errorf("decode GitHub App private key PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse GitHub App private key: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("GitHub App private key must be RSA")
	}
	return key, nil
}

func jwtSegment(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}

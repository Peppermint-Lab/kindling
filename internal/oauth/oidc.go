package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type OIDCConfig struct {
	ClientID     string
	ClientSecret string
	IssuerURL    string
	RedirectURL  string
	Scopes       []string
}

func OIDCAuthorizeURL(ctx context.Context, client *http.Client, cfg OIDCConfig, state, nonce, codeChallenge string) (string, error) {
	provider, oauthConfig, err := oidcProvider(ctx, client, cfg)
	if err != nil {
		return "", err
	}
	_ = provider
	return oauthConfig.AuthCodeURL(
		state,
		gooidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	), nil
}

func ExchangeOIDCCode(ctx context.Context, client *http.Client, cfg OIDCConfig, code, verifier, nonce string) (Identity, error) {
	provider, oauthConfig, err := oidcProvider(ctx, client, cfg)
	if err != nil {
		return Identity{}, err
	}
	ctx = oidcContext(ctx, client)
	token, err := oauthConfig.Exchange(ctx, strings.TrimSpace(code), oauth2.SetAuthURLParam("code_verifier", verifier))
	if err != nil {
		return Identity{}, err
	}
	rawIDToken, _ := token.Extra("id_token").(string)
	if strings.TrimSpace(rawIDToken) == "" {
		return Identity{}, fmt.Errorf("oidc token response missing id_token")
	}
	idToken, err := provider.Verifier(&gooidc.Config{ClientID: strings.TrimSpace(cfg.ClientID)}).Verify(ctx, rawIDToken)
	if err != nil {
		return Identity{}, err
	}

	claimsMap := map[string]any{}
	if err := idToken.Claims(&claimsMap); err != nil {
		return Identity{}, err
	}
	var claims struct {
		Subject           string `json:"sub"`
		Email             string `json:"email"`
		EmailVerified     *bool  `json:"email_verified"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Nonce             string `json:"nonce"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return Identity{}, err
	}
	if strings.TrimSpace(nonce) != "" && claims.Nonce != strings.TrimSpace(nonce) {
		return Identity{}, fmt.Errorf("oidc nonce mismatch")
	}

	email := strings.TrimSpace(claims.Email)
	if claims.EmailVerified != nil && !*claims.EmailVerified {
		email = ""
	}
	login := strings.TrimSpace(claims.PreferredUsername)
	displayName := strings.TrimSpace(claims.Name)

	if email == "" || login == "" || displayName == "" {
		userInfo, err := provider.UserInfo(ctx, oauth2.StaticTokenSource(token))
		if err == nil && userInfo != nil {
			var infoClaims struct {
				Subject           string `json:"sub"`
				Email             string `json:"email"`
				EmailVerified     *bool  `json:"email_verified"`
				Name              string `json:"name"`
				PreferredUsername string `json:"preferred_username"`
			}
			if err := userInfo.Claims(&infoClaims); err == nil {
				if strings.TrimSpace(infoClaims.Subject) != "" && infoClaims.Subject != claims.Subject {
					return Identity{}, fmt.Errorf("oidc userinfo subject mismatch")
				}
				if email == "" && strings.TrimSpace(infoClaims.Email) != "" {
					if infoClaims.EmailVerified == nil || *infoClaims.EmailVerified {
						email = strings.TrimSpace(infoClaims.Email)
					}
				}
				if login == "" {
					login = strings.TrimSpace(infoClaims.PreferredUsername)
				}
				if displayName == "" {
					displayName = strings.TrimSpace(infoClaims.Name)
				}
			}
			var rawUserInfo map[string]any
			if err := userInfo.Claims(&rawUserInfo); err == nil {
				claimsMap["userinfo"] = rawUserInfo
			}
		}
	}

	if displayName == "" {
		displayName = login
	}
	if displayName == "" {
		displayName = email
	}

	return Identity{
		Subject:     strings.TrimSpace(claims.Subject),
		Email:       email,
		Login:       login,
		DisplayName: displayName,
		Claims:      claimsMap,
	}, nil
}

func oidcProvider(ctx context.Context, client *http.Client, cfg OIDCConfig) (*gooidc.Provider, oauth2.Config, error) {
	ctx = oidcContext(ctx, client)
	provider, err := gooidc.NewProvider(ctx, strings.TrimSpace(cfg.IssuerURL))
	if err != nil {
		return nil, oauth2.Config{}, err
	}
	oauthConfig := oauth2.Config{
		ClientID:     strings.TrimSpace(cfg.ClientID),
		ClientSecret: strings.TrimSpace(cfg.ClientSecret),
		Endpoint:     provider.Endpoint(),
		RedirectURL:  strings.TrimSpace(cfg.RedirectURL),
		Scopes:       append([]string(nil), cfg.Scopes...),
	}
	return provider, oauthConfig, nil
}

func oidcContext(ctx context.Context, client *http.Client) context.Context {
	if client == nil {
		return ctx
	}
	return context.WithValue(ctx, oauth2.HTTPClient, client)
}

func OIDCClaimsJSON(claims map[string]any) []byte {
	b, err := json.Marshal(claims)
	if err != nil {
		return []byte(`{}`)
	}
	return b
}

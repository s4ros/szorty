package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"szorty.infinigy.pl/internal/config"
	"szorty.infinigy.pl/internal/httpapp"
)

// googleExchanger exchanges an authorization code at Google's token endpoint.
// Redirect URL, scopes, and endpoint match the URL httpapp sends to Google.
type googleExchanger struct {
	conf *oauth2.Config
}

func newGoogleExchanger(cfg config.Config) *googleExchanger {
	return &googleExchanger{conf: &oauth2.Config{
		ClientID:     cfg.GoogleClientID,
		ClientSecret: cfg.GoogleClientSecret,
		RedirectURL:  strings.TrimRight(cfg.PublicBaseURL, "/") + "/auth/callback",
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     google.Endpoint,
	}}
}

var _ httpapp.Exchanger = (*googleExchanger)(nil)

func (g *googleExchanger) Exchange(ctx context.Context, code string) (httpapp.Identity, error) {
	tok, err := g.conf.Exchange(ctx, code)
	if err != nil {
		return httpapp.Identity{}, err
	}
	// The token endpoint authenticated this response. The id_token claims are
	// read from that body; nothing here dials a second Google URL.
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return httpapp.Identity{}, errors.New("missing id_token")
	}
	return identityFromIDToken(raw)
}

func identityFromIDToken(raw string) (httpapp.Identity, error) {
	parts := strings.Split(raw, ".")
	if len(parts) < 2 {
		return httpapp.Identity{}, errors.New("id_token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return httpapp.Identity{}, err
		}
	}
	var claims struct {
		Sub           string          `json:"sub"`
		Email         string          `json:"email"`
		EmailVerified json.RawMessage `json:"email_verified"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return httpapp.Identity{}, err
	}
	return httpapp.Identity{
		Sub:           strings.TrimSpace(claims.Sub),
		Email:         strings.TrimSpace(claims.Email),
		EmailVerified: claimBool(claims.EmailVerified),
	}, nil
}

// claimBool accepts the boolean Google puts in an id_token and the string
// form some userinfo responses use.
func claimBool(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s == "true"
	}
	return false
}

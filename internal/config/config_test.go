package config

import (
	"strings"
	"testing"
)

func fullEnv(overrides map[string]string) func(string) string {
	values := map[string]string{
		"GOOGLE_CLIENT_ID":     "client-id",
		"GOOGLE_CLIENT_SECRET": "client-secret",
		"SESSION_SECRET":       strings.Repeat("s", 32),
		"OWNER_EMAIL":          "owner@example.com",
		"PUBLIC_BASE_URL":      "https://szorty.infinigy.pl",
		"DB_PATH":              "/data/szort.db",
		"LISTEN_ADDR":          ":8080",
	}
	for key, value := range overrides {
		values[key] = value
	}
	return func(key string) string {
		return values[key]
	}
}

func TestLoadMissingVar(t *testing.T) {
	names := []string{
		"GOOGLE_CLIENT_ID",
		"GOOGLE_CLIENT_SECRET",
		"SESSION_SECRET",
		"OWNER_EMAIL",
		"PUBLIC_BASE_URL",
		"DB_PATH",
		"LISTEN_ADDR",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			_, err := Load(fullEnv(map[string]string{name: ""}))
			if err == nil {
				t.Fatalf("Load accepted a missing %s", name)
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("error %q does not contain %s", err.Error(), name)
			}
			if strings.Contains(err.Error(), "client-secret") || strings.Contains(err.Error(), strings.Repeat("s", 32)) {
				t.Fatalf("error leaked another variable: %s", err.Error())
			}
		})
	}
}

func TestLoadShortSecret(t *testing.T) {
	secret := strings.Repeat("s", 31)
	_, err := Load(fullEnv(map[string]string{"SESSION_SECRET": secret}))
	if err == nil {
		t.Fatal("Load accepted a SESSION_SECRET shorter than 32 bytes")
	}
	if !strings.Contains(err.Error(), "SESSION_SECRET") {
		t.Fatalf("error %q does not contain SESSION_SECRET", err.Error())
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked SESSION_SECRET: %s", err.Error())
	}
}

func TestLoadBaseURL(t *testing.T) {
	tests := []struct {
		raw string
		ok  bool
	}{
		{raw: "http://example.com", ok: false},
		{raw: "http://127.0.0.1:8080", ok: true},
		{raw: "http://localhost:8080", ok: true},
		{raw: "https://szorty.infinigy.pl", ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			cfg, err := Load(fullEnv(map[string]string{"PUBLIC_BASE_URL": tt.raw}))
			if tt.ok {
				if err != nil {
					t.Fatalf("Load rejected %s: %v", tt.raw, err)
				}
				if cfg.PublicBaseURL != tt.raw {
					t.Fatalf("PublicBaseURL = %q, want %q", cfg.PublicBaseURL, tt.raw)
				}
				return
			}
			if err == nil {
				t.Fatalf("Load accepted %s", tt.raw)
			}
			if !strings.Contains(err.Error(), "PUBLIC_BASE_URL") {
				t.Fatalf("error %q does not contain PUBLIC_BASE_URL", err.Error())
			}
		})
	}
}

func TestLoadSuccess(t *testing.T) {
	secret := strings.Repeat("s", 32)
	cfg, err := Load(fullEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GoogleClientID != "client-id" ||
		cfg.GoogleClientSecret != "client-secret" ||
		cfg.OwnerEmail != "owner@example.com" ||
		cfg.PublicBaseURL != "https://szorty.infinigy.pl" ||
		cfg.DBPath != "/data/szort.db" ||
		cfg.ListenAddr != ":8080" ||
		string(cfg.SessionSecret) != secret {
		t.Fatalf("Load = %+v", cfg)
	}
}

package config

import (
	"errors"
	"net/url"
	"strings"
)

// Config is the process configuration.
type Config struct {
	GoogleClientID     string
	GoogleClientSecret string
	OwnerEmail         string
	PublicBaseURL      string
	DBPath             string
	ListenAddr         string
	SessionSecret      []byte
}

// Load reads configuration through getenv. A missing variable's error text is
// only that variable's name. SESSION_SECRET must be at least 32 bytes.
// PUBLIC_BASE_URL must be https, except http on localhost or 127.0.0.1.
func Load(getenv func(string) string) (Config, error) {
	var cfg Config
	fields := []struct {
		name string
		dst  *string
	}{
		{name: "GOOGLE_CLIENT_ID", dst: &cfg.GoogleClientID},
		{name: "GOOGLE_CLIENT_SECRET", dst: &cfg.GoogleClientSecret},
		{name: "OWNER_EMAIL", dst: &cfg.OwnerEmail},
		{name: "PUBLIC_BASE_URL", dst: &cfg.PublicBaseURL},
		{name: "DB_PATH", dst: &cfg.DBPath},
		{name: "LISTEN_ADDR", dst: &cfg.ListenAddr},
	}
	for _, field := range fields {
		value := getenv(field.name)
		if value == "" {
			return Config{}, errors.New(field.name)
		}
		*field.dst = value
	}

	secret := getenv("SESSION_SECRET")
	if secret == "" {
		return Config{}, errors.New("SESSION_SECRET")
	}
	if len(secret) < 32 {
		return Config{}, errors.New("SESSION_SECRET shorter than 32 bytes")
	}
	cfg.SessionSecret = []byte(secret)

	if err := validatePublicBaseURL(cfg.PublicBaseURL); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func validatePublicBaseURL(raw string) error {
	u, err := url.Parse(raw)
	host := ""
	if err == nil {
		host = u.Hostname()
	}
	if err == nil && u.Scheme == "https" && host != "" {
		return nil
	}
	// Local runs may use http. Every other host must be https.
	if err == nil && u.Scheme == "http" && host != "" && (host == "127.0.0.1" || strings.EqualFold(host, "localhost")) {
		return nil
	}
	return errors.New("PUBLIC_BASE_URL must be https, or http on localhost or 127.0.0.1")
}

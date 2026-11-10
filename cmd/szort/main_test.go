package main

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestMissingConfigPrintsNameOnly(t *testing.T) {
	var buf bytes.Buffer
	code := mainExit(func(string) string { return "" }, &buf)
	if code == 0 {
		t.Fatal("exit status 0")
	}
	if buf.String() != "GOOGLE_CLIENT_ID\n" {
		t.Fatalf("stderr = %q", buf.String())
	}
}

func TestMissingLaterConfigPrintsThatNameOnly(t *testing.T) {
	var buf bytes.Buffer
	code := mainExit(func(k string) string {
		switch k {
		case "DB_PATH":
			return ""
		case "SESSION_SECRET":
			return strings.Repeat("s", 32)
		case "PUBLIC_BASE_URL":
			return "https://szorty.infinigy.pl"
		default:
			return "set"
		}
	}, &buf)
	if code == 0 {
		t.Fatal("exit status 0")
	}
	if buf.String() != "DB_PATH\n" {
		t.Fatalf("stderr = %q", buf.String())
	}
}

func TestSweepEveryFifteenMinutes(t *testing.T) {
	if sweepInterval != 15*time.Minute {
		t.Fatalf("sweep interval = %s, want 15m", sweepInterval)
	}
}

func TestIdentityFromIDToken(t *testing.T) {
	id, err := identityFromIDToken(jwt(`{"sub":"abc","email":"a@b.c","email_verified":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if id.Sub != "abc" || id.Email != "a@b.c" || !id.EmailVerified {
		t.Fatalf("identity = %+v", id)
	}

	unverified, err := identityFromIDToken(jwt(`{"sub":"abc","email":"a@b.c","email_verified":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if unverified.EmailVerified {
		t.Fatal("false claim was verified")
	}

	asString, err := identityFromIDToken(jwt(`{"sub":"abc","email":"a@b.c","email_verified":"true"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !asString.EmailVerified {
		t.Fatal(`string "true" was not verified`)
	}

	if _, err := identityFromIDToken("not-a-jwt"); err == nil {
		t.Fatal("malformed token returned nil error")
	}
}

func jwt(payload string) string {
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".sig"
}

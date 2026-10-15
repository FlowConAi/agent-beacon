// Copyright (c) 2026 FlowConAI UG (haftungsbeschränkt). All rights reserved.
package heedvane

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestURLAndBrandResolution(t *testing.T) {
	t.Setenv(EnvHeedvaneMode, "")
	t.Setenv(EnvHeedvaneServerURL, "")
	t.Setenv(EnvHeedvaneDashboard, "")
	t.Setenv(EnvHeedvaneAuthURL, "")
	t.Setenv(EnvHeedvaneEnrollToken, "")
	t.Setenv(EnvHeedvaneListenerURL, "")
	t.Setenv("BEACON_DASHBOARD_URL", "")
	t.Setenv("BEACON_AUTH_URL", "")

	if Enabled() {
		t.Fatal("expected Enabled() to be false when no Heedvane env vars are set")
	}
	if got := BrandName("Beacon"); got != "Beacon" {
		t.Fatalf("expected legacy brand Beacon, got %q", got)
	}
	if got := ResolveDashboardURL("", "BEACON_DASHBOARD_URL", "https://beacon.sh"); got != "https://beacon.sh" {
		t.Fatalf("expected legacy dashboard URL, got %q", got)
	}

	t.Setenv(EnvHeedvaneMode, "1")
	if !Enabled() {
		t.Fatal("expected Enabled() to be true when HEEDVANE_MODE=1")
	}
	if got := BrandName("Beacon"); got != "Heedvane" {
		t.Fatalf("expected Heedvane brand, got %q", got)
	}
	if got := ResolveDashboardURL("", "BEACON_DASHBOARD_URL", "https://beacon.sh"); got != DefaultHeedvaneURL {
		t.Fatalf("expected DefaultHeedvaneURL, got %q", got)
	}
	if got := ResolveAuthURL("", "BEACON_AUTH_URL", "https://beacon.sh"); got != DefaultHeedvaneURL {
		t.Fatalf("expected DefaultHeedvaneURL for auth, got %q", got)
	}

	t.Setenv(EnvHeedvaneServerURL, "https://app.heedvane.ai/")
	if got := ResolveDashboardURL("", "BEACON_DASHBOARD_URL", "https://beacon.sh"); got != "https://app.heedvane.ai" {
		t.Fatalf("expected trimmed HEEDVANE_SERVER_URL, got %q", got)
	}
}

func TestTokenAndDeviceKeyValidation(t *testing.T) {
	for _, validToken := range []string{
		"hdv_cli_1234567890abcdef",
		"hdv_enroll_1234567890abcdef",
		"bcn_cli_1234567890abcdef",
	} {
		if !IsValidAccessToken(validToken) {
			t.Errorf("expected %q to be a valid access token", validToken)
		}
	}
	if IsValidAccessToken("invalid_token") {
		t.Error("expected invalid_token to be rejected")
	}

	for _, validKey := range []string{
		"hdv_device_1234567890abcdef",
		"bcn_device_1234567890abcdef",
	} {
		if !IsValidDeviceKey(validKey) {
			t.Errorf("expected %q to be a valid device key", validKey)
		}
	}
	if IsValidDeviceKey("other_key") {
		t.Error("expected other_key to be rejected")
	}
}

func TestNoAutoAllowEnabled(t *testing.T) {
	t.Setenv(EnvHeedvaneMode, "")
	t.Setenv(EnvHeedvaneNoAutoAllow, "")
	if NoAutoAllowEnabled() {
		t.Fatal("expected NoAutoAllowEnabled() false by default when Heedvane mode is off")
	}

	t.Setenv(EnvHeedvaneMode, "1")
	if !NoAutoAllowEnabled() {
		t.Fatal("expected NoAutoAllowEnabled() true when HEEDVANE_MODE=1")
	}

	t.Setenv(EnvHeedvaneNoAutoAllow, "0")
	if NoAutoAllowEnabled() {
		t.Fatal("expected NoAutoAllowEnabled() false when explicitly disabled")
	}
}

func TestRedactSecrets(t *testing.T) {
	samples := []string{
		"ghp_1234567890abcdefghij1234567890",
		"github_pat_11ABCDEF0123456789_abcdefghijklmnop",
		"xoxb-1234567890-abcdefghij",
		"AKIAIOSFODNN7EXAMPLE",
		"sk-ant-api03-1234567890abcdef1234567890",
		"hdv_device_1234567890abcdef",
		"bcn_cli_1234567890abcdef",
		"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA...\n-----END RSA PRIVATE KEY-----",
	}
	for _, sample := range samples {
		redacted := RedactSecrets("prefix " + sample + " suffix")
		if strings.Contains(redacted, sample) {
			t.Errorf("expected secret %q to be redacted, got %q", sample, redacted)
		}
		if !strings.Contains(redacted, "[REDACTED]") {
			t.Errorf("expected [REDACTED] marker in %q", redacted)
		}
	}
}

func TestUserLogPermissions(t *testing.T) {
	if UserLogDirPerm() != os.FileMode(0o700) {
		t.Fatalf("expected 0700 dir perm, got %o", UserLogDirPerm())
	}
	if UserLogFilePerm() != os.FileMode(0o600) {
		t.Fatalf("expected 0600 file perm, got %o", UserLogFilePerm())
	}
}

func TestForwardHookEvent(t *testing.T) {
	var received map[string]interface{}
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv(EnvHeedvaneListenerURL, srv.URL)
	t.Setenv(EnvHeedvaneListenerTok, "secret-listener-token")

	ForwardHookEvent(
		"tool.invoked",
		"claude",
		map[string]interface{}{"tool_name": "Bash"},
		map[string]interface{}{"branch": "main"},
	)

	if authHeader != "Bearer secret-listener-token" {
		t.Fatalf("expected Bearer auth header, got %q", authHeader)
	}
	if received["action"] != "tool.invoked" || received["platform"] != "claude" {
		t.Fatalf("unexpected forwarded payload: %#v", received)
	}
}

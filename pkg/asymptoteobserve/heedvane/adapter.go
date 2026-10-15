// Copyright (c) 2026 FlowConAI UG (haftungsbeschränkt). All rights reserved.
// Represented by Managing Director: Andre Machon.
//
// Package heedvane isolates Heedvane-specific customizations (branding, URL
// resolution, headless token enrolment, no-auto-allow policy defaults, log
// permission tightening, expanded secret redaction, and unified listener hook
// forwarding) behind a narrow adapter so upstream updates from agent-beacon
// can be merged cleanly with minimal conflict.
package heedvane

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

const (
	// DefaultHeedvaneURL is the canonical Heedvane base URL when Heedvane mode is active.
	DefaultHeedvaneURL = "https://heedvane.ai"

	EnvHeedvaneMode        = "HEEDVANE_MODE"
	EnvHeedvaneServerURL   = "HEEDVANE_SERVER_URL"
	EnvHeedvaneDashboard   = "HEEDVANE_DASHBOARD_URL"
	EnvHeedvaneAuthURL     = "HEEDVANE_AUTH_URL"
	EnvHeedvaneEnrollToken = "HEEDVANE_ENROLL_TOKEN"
	EnvAsymptoteEnroll     = "ASYMPTOTE_ENROLL_TOKEN"
	EnvHeedvaneNoAutoAllow = "HEEDVANE_NO_AUTO_ALLOW"
	EnvHeedvaneListenerURL = "HEEDVANE_LISTENER_URL"
	EnvHeedvaneListenerTok = "HEEDVANE_LISTENER_TOKEN"

	userLogDirPerm  os.FileMode = 0o700
	userLogFilePerm os.FileMode = 0o600
)

var extraSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`\bsk-proj-[A-Za-z0-9_-]{16,}\b`),
	regexp.MustCompile(`\b(?:hdv|bcn)_(?:device|cli|enroll)_[A-Za-z0-9_-]{12,}\b`),
}

// Enabled reports whether Heedvane adapter mode is active via environment configuration.
func Enabled() bool {
	if isTruthyEnv(EnvHeedvaneMode) {
		return true
	}
	for _, key := range []string{
		EnvHeedvaneServerURL,
		EnvHeedvaneDashboard,
		EnvHeedvaneAuthURL,
		EnvHeedvaneEnrollToken,
		EnvHeedvaneListenerURL,
	} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}

// BrandName returns "Heedvane" when Heedvane mode is active, or the provided legacy brand name.
func BrandName(legacyDefault string) string {
	if Enabled() {
		return "Heedvane"
	}
	return legacyDefault
}

// ResolveDashboardURL resolves the dashboard URL from flags, Heedvane env vars, legacy env vars, or defaults.
func ResolveDashboardURL(flagValue, legacyEnvVar, legacyDefault string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return trimTrailingSlash(v)
	}
	for _, envKey := range []string{EnvHeedvaneDashboard, EnvHeedvaneServerURL, legacyEnvVar} {
		if envKey == "" {
			continue
		}
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			return trimTrailingSlash(v)
		}
	}
	if Enabled() {
		return DefaultHeedvaneURL
	}
	return trimTrailingSlash(legacyDefault)
}

// ResolveAuthURL resolves the CLI authentication base URL from flags, Heedvane env vars, legacy env vars, or defaults.
func ResolveAuthURL(flagValue, legacyEnvVar, legacyDefault string) string {
	if v := strings.TrimSpace(flagValue); v != "" {
		return trimTrailingSlash(v)
	}
	for _, envKey := range []string{EnvHeedvaneAuthURL, EnvHeedvaneDashboard, EnvHeedvaneServerURL, legacyEnvVar} {
		if envKey == "" {
			continue
		}
		if v := strings.TrimSpace(os.Getenv(envKey)); v != "" {
			return trimTrailingSlash(v)
		}
	}
	if Enabled() {
		return DefaultHeedvaneURL
	}
	return trimTrailingSlash(legacyDefault)
}

// EnrollTokenFromEnv returns a headless enrolment token from HEEDVANE_ENROLL_TOKEN or ASYMPTOTE_ENROLL_TOKEN.
func EnrollTokenFromEnv() string {
	if tok := strings.TrimSpace(os.Getenv(EnvHeedvaneEnrollToken)); tok != "" {
		return tok
	}
	return strings.TrimSpace(os.Getenv(EnvAsymptoteEnroll))
}

// IsValidAccessToken reports whether token has a valid CLI/enrolment token prefix
// (accepting both Heedvane hdv_cli_/hdv_enroll_ and internal bcn_cli_ prefixes).
func IsValidAccessToken(token string) bool {
	trimmed := strings.TrimSpace(token)
	return strings.HasPrefix(trimmed, "hdv_cli_") ||
		strings.HasPrefix(trimmed, "hdv_enroll_") ||
		strings.HasPrefix(trimmed, "bcn_cli_")
}

// IsValidDeviceKey reports whether key has a valid device ingest key prefix
// (accepting both Heedvane hdv_device_ and internal bcn_device_ prefixes).
func IsValidDeviceKey(key string) bool {
	trimmed := strings.TrimSpace(key)
	return strings.HasPrefix(trimmed, "hdv_device_") ||
		strings.HasPrefix(trimmed, "bcn_device_")
}

// NoAutoAllowEnabled reports whether hooks must abstain from returning an automatic
// "allow" / "approve" decision when no policy rule matched, leaving the agent harness's
// native user approval prompt intact.
func NoAutoAllowEnabled() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(EnvHeedvaneNoAutoAllow)))
	if raw == "0" || raw == "false" || raw == "no" {
		return false
	}
	if raw == "1" || raw == "true" || raw == "yes" {
		return true
	}
	return Enabled()
}

// UserLogDirPerm returns the tightened directory mode (0700) for local user log/state directories.
func UserLogDirPerm() os.FileMode {
	return userLogDirPerm
}

// UserLogFilePerm returns the tightened file mode (0600) for local user log files.
func UserLogFilePerm() os.FileMode {
	return userLogFilePerm
}

// RedactSecrets scrubs high-entropy developer credentials, cloud keys, PEM private keys,
// and Heedvane/Beacon tokens from telemetry strings before disk write or upload.
func RedactSecrets(value string) string {
	if value == "" {
		return value
	}
	for _, pattern := range extraSecretPatterns {
		value = pattern.ReplaceAllString(value, "[REDACTED]")
	}
	return value
}

// ForwardHookEvent forwards a normalized hook event to the configured Heedvane listener
// (when HEEDVANE_LISTENER_URL is set) using a bounded 250ms timeout so a single hook
// installation can feed both the collector and the Heedvane governance listener.
func ForwardHookEvent(action, platform string, input, fields map[string]interface{}) {
	listenerURL := strings.TrimSpace(os.Getenv(EnvHeedvaneListenerURL))
	if listenerURL == "" {
		return
	}
	payload := map[string]interface{}{
		"action":    action,
		"platform":  platform,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"fields":    fields,
		"input":     input,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, listenerURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := strings.TrimSpace(os.Getenv(EnvHeedvaneListenerTok)); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err == nil && resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

func isTruthyEnv(key string) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	return v == "1" || v == "true" || v == "yes"
}

func trimTrailingSlash(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

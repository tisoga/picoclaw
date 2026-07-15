package oauthprovider

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchAntigravityQuotaNormalizesAndFiltersModels(t *testing.T) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer token-1" {
			t.Fatalf("Authorization = %q", got)
		}
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "antigravity/ide/2.1.1 ") {
			t.Fatalf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{
				"cloudaicompanionProject":"project-from-api",
				"currentTier":{"name":"Google AI Pro"}
			}`))
		case "/v1internal:fetchAvailableModels":
			_, _ = w.Write([]byte(`{"models":{
				"claude-sonnet-4-6":{"displayName":"Claude Sonnet 4.6","quotaInfo":{"remainingFraction":"0.25","resetTime":"2026-07-15T12:00:00Z"}},
				"gemini-3-flash-agent":{"displayName":"Gemini 3.5 Flash (High)","quotaInfo":{"remainingFraction":0.8,"resetTime":"2026-07-15T11:00:00Z"}},
				"gemini-pro-agent":{"displayName":"Internal","isInternal":true,"quotaInfo":{"remainingFraction":1}},
				"unknown-model":{"displayName":"Unknown","quotaInfo":{"remainingFraction":1}},
				"gemini-3-flash":{"displayName":"No quota"}
			}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	snapshot, err := fetchAntigravityQuota(
		&http.Client{Timeout: time.Second}, server.URL, "token-1", "stored-project",
	)
	if err != nil {
		t.Fatalf("fetchAntigravityQuota() error = %v", err)
	}
	if snapshot.Plan != "Google AI Pro" || snapshot.ProjectID != "project-from-api" {
		t.Fatalf("snapshot metadata = %+v", snapshot)
	}
	if len(snapshot.Models) != 2 {
		t.Fatalf("models = %+v, want 2 recommended real quota buckets", snapshot.Models)
	}
	if snapshot.Models[0].ID != "gemini-3-flash-agent" || snapshot.Models[1].ID != "claude-sonnet-4-6" {
		t.Fatalf("model order = %+v", snapshot.Models)
	}
	if snapshot.Models[0].RemainingFraction != 0.8 || snapshot.Models[0].IsExhausted {
		t.Fatalf("first model = %+v", snapshot.Models[0])
	}
	if snapshot.Models[1].RemainingFraction != 0.25 {
		t.Fatalf("string remaining fraction was not parsed: %+v", snapshot.Models[1])
	}
}

func TestFetchAntigravityQuotaUsesStoredProjectWhenSubscriptionUnavailable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1internal:loadCodeAssist" {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"models":{}}`))
	}))
	defer server.Close()

	snapshot, err := fetchAntigravityQuota(
		&http.Client{Timeout: time.Second}, server.URL, "token", "stored-project",
	)
	if err != nil {
		t.Fatalf("fetchAntigravityQuota() error = %v", err)
	}
	if snapshot.ProjectID != "stored-project" || snapshot.Plan != "Unknown" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestFetchAntigravityQuotaPreservesAuthStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "expired", http.StatusUnauthorized)
	}))
	defer server.Close()

	_, err := fetchAntigravityQuota(
		&http.Client{Timeout: time.Second}, server.URL, "bad-token", "",
	)
	var apiErr *AntigravityAPIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("error = %#v, want AntigravityAPIError(401)", err)
	}
}

func TestParseRemainingFractionClampsValues(t *testing.T) {
	tests := []struct {
		raw  string
		want float64
	}{
		{raw: `0.5`, want: 0.5},
		{raw: `"0.75"`, want: 0.75},
		{raw: `-1`, want: 0},
		{raw: `2`, want: 1},
	}
	for _, tt := range tests {
		got, err := parseRemainingFraction([]byte(tt.raw))
		if err != nil || got != tt.want {
			t.Fatalf("parseRemainingFraction(%s) = %v, %v; want %v", tt.raw, got, err, tt.want)
		}
	}
}

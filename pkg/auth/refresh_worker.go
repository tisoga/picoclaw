package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/sipeed/picoclaw/pkg/logger"
)

// StartRefreshWorker starts a background goroutine that proactively checks and
// refreshes OAuth tokens before they expire. It loops every minute.
func StartRefreshWorker(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		// Run once immediately
		refreshAllCredentials()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refreshAllCredentials()
			}
		}
	}()
}

func refreshAllCredentials() {
	store, err := LoadStore()
	if err != nil {
		return // Cannot load store, wait for next tick
	}

	for provider, cred := range store.Credentials {
		// Only try to refresh if it's an OAuth credential with a refresh token
		if cred.AuthMethod == "oauth" || cred.AuthMethod == "browser" || cred.AuthMethod == "device_code" {
			if cred.RefreshToken != "" && cred.NeedsRefresh() {
				// Don't keep hammering if it's already far past expired and failing
				if cred.IsExpired() && time.Since(cred.ExpiresAt) > 24*time.Hour {
					continue
				}

				if err := refreshSingleCredential(provider, cred); err != nil {
					logger.WarnCF("auth", fmt.Sprintf("failed to proactively refresh token for %s", provider), map[string]any{
						"error": err.Error(),
					})
				} else {
					logger.InfoCF("auth", fmt.Sprintf("proactively refreshed token for %s", provider), nil)
				}
			}
		}
	}
}

func refreshSingleCredential(provider string, cred *AuthCredential) error {
	var cfg OAuthProviderConfig
	switch canonicalProvider(provider) {
	case "openai":
		cfg = OpenAIOAuthConfig()
	case "google-antigravity":
		cfg = GoogleAntigravityOAuthConfig()
	default:
		return fmt.Errorf("no oauth config for provider %s", provider)
	}

	newCred, err := RefreshAccessToken(cred, cfg)
	if err != nil {
		return err
	}
	
	newCred.Provider = canonicalProvider(provider)
	return SetCredential(newCred.Provider, newCred)
}

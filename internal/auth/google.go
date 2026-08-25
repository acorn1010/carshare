package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Profile is what we keep from a Google sign-in. Sub is Google's stable
// subject id and is the upsert key, so email changes never fork an account.
type Profile struct {
	Sub     string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

// Provider is the OAuth surface the HTTP layer needs. Tests use a stub.
type Provider interface {
	AuthCodeURL(state string) string
	FetchProfile(ctx context.Context, code string) (Profile, error)
}

// Google implements Provider against real Google OAuth.
type Google struct {
	config *oauth2.Config
}

var _ Provider = (*Google)(nil)

// userinfoURL is Google's OpenID Connect userinfo endpoint.
const userinfoURL = "https://openidconnect.googleapis.com/v1/userinfo"

// NewGoogle builds the provider. redirectURL must match the console exactly,
// for production that is https://cars.foony.com/v1/auth/google/callback.
func NewGoogle(clientID, clientSecret, redirectURL string) *Google {
	return &Google{config: &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Scopes:       []string{"openid", "email", "profile"},
		Endpoint:     google.Endpoint,
	}}
}

// AuthCodeURL is where the browser goes to pick an account.
func (provider *Google) AuthCodeURL(state string) string {
	return provider.config.AuthCodeURL(state)
}

// FetchProfile trades the callback code for the user's profile.
func (provider *Google) FetchProfile(ctx context.Context, code string) (Profile, error) {
	token, err := provider.config.Exchange(ctx, code)
	if err != nil {
		return Profile{}, fmt.Errorf("auth: code exchange: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, userinfoURL, nil)
	if err != nil {
		return Profile{}, fmt.Errorf("auth: userinfo request: %w", err)
	}
	token.SetAuthHeader(request)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return Profile{}, fmt.Errorf("auth: userinfo: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return Profile{}, fmt.Errorf("auth: userinfo status %d", response.StatusCode)
	}
	var profile Profile
	if err := json.NewDecoder(response.Body).Decode(&profile); err != nil {
		return Profile{}, fmt.Errorf("auth: userinfo decode: %w", err)
	}
	if profile.Sub == "" {
		return Profile{}, fmt.Errorf("auth: userinfo missing sub")
	}
	return profile, nil
}

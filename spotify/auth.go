package spotify

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	authEndpoint  = "https://accounts.spotify.com/authorize"
	tokenEndpoint = "https://accounts.spotify.com/api/token"

	// Scopes required for playback control, reading/modifying playlists/shows, and
	// Spotify Connect device registration via librespot.
	scopes = "user-read-playback-state user-modify-playback-state playlist-read-private playlist-read-collaborative playlist-modify-public playlist-modify-private streaming"
)

// Token holds a Spotify OAuth token pair.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// valid reports whether the access token is present and not expired.
func (t *Token) valid() bool {
	return t != nil && t.AccessToken != "" && time.Now().Before(t.ExpiresAt)
}

// TokenStore persists and retrieves a Token.
// Implement this interface to swap in a different backend (e.g. SQLite in Phase 9).
type TokenStore interface {
	Load() (*Token, error)
	Save(*Token) error
	DeleteToken() error
}

// FileTokenStore implements TokenStore using a JSON file on disk.
type FileTokenStore struct {
	path string
}

func NewFileTokenStore(path string) *FileTokenStore {
	return &FileTokenStore{path: path}
}

func (s *FileTokenStore) Load() (*Token, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("spotify: load token file: %w", err)
	}
	var t Token
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("spotify: parse token file: %w", err)
	}
	return &t, nil
}

func (s *FileTokenStore) DeleteToken() error {
	err := os.Remove(s.path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s *FileTokenStore) Save(t *Token) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("spotify: marshal token: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0600); err != nil {
		return fmt.Errorf("spotify: write token file: %w", err)
	}
	return nil
}

// Auth manages the Spotify Authorization Code flow and token lifecycle.
type Auth struct {
	clientID     string
	clientSecret string
	redirectURI  string
	store        TokenStore

	mu    sync.Mutex
	token *Token
	state string // expected OAuth state, set during an in-flight auth flow
}

// NewAuth creates an Auth and loads any previously persisted token from store.
func NewAuth(clientID, clientSecret, redirectURI string, store TokenStore) (*Auth, error) {
	a := &Auth{
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURI:  redirectURI,
		store:        store,
	}
	t, err := store.Load()
	if err != nil {
		return nil, err
	}
	a.token = t
	if t != nil {
		slog.Info("spotify: loaded persisted token", "expires_at", t.ExpiresAt)
	}
	return a, nil
}

// Logout clears the in-memory token and removes it from the store.
// After this call HasToken returns false and the user must re-authorize.
func (a *Auth) Logout() error {
	if err := a.store.DeleteToken(); err != nil {
		return err
	}
	a.mu.Lock()
	a.token = nil
	a.mu.Unlock()
	return nil
}

// HasToken reports whether a token (possibly expired) is available. A true
// result means the OAuth flow has been completed at least once; false means
// the user must visit /auth.
func (a *Auth) HasToken() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.token != nil && a.token.RefreshToken != ""
}

// AuthURL generates a Spotify authorization URL and stores the random state
// value to validate when the callback arrives.
func (a *Auth) AuthURL() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("spotify: generate state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(b)

	a.mu.Lock()
	a.state = state
	a.mu.Unlock()

	params := url.Values{
		"client_id":     {a.clientID},
		"response_type": {"code"},
		"redirect_uri":  {a.redirectURI},
		"scope":         {scopes},
		"state":         {state},
	}
	return authEndpoint + "?" + params.Encode(), nil
}

// Exchange completes the OAuth flow using the authorization code and state
// returned by Spotify. The state must match the value from the last AuthURL call.
func (a *Auth) Exchange(ctx context.Context, code, state string) error {
	a.mu.Lock()
	expected := a.state
	a.mu.Unlock()

	if state == "" || state != expected {
		return fmt.Errorf("spotify: OAuth state mismatch")
	}

	t, err := a.requestToken(ctx, url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {a.redirectURI},
	})
	if err != nil {
		return err
	}
	return a.persist(t)
}

// AccessToken returns a valid access token, transparently refreshing it if
// it has expired. Returns an error if no token is available (user must
// complete the OAuth flow via /auth).
func (a *Auth) AccessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	t := a.token
	a.mu.Unlock()

	if t == nil || t.RefreshToken == "" {
		return "", fmt.Errorf("spotify: not authenticated - visit /auth to authorize")
	}
	if t.valid() {
		return t.AccessToken, nil
	}

	slog.Info("spotify: access token expired, refreshing")
	refreshed, err := a.requestToken(ctx, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
	})
	if err != nil {
		return "", fmt.Errorf("spotify: refresh token: %w", err)
	}
	// Spotify may not return a new refresh token; preserve the existing one.
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = t.RefreshToken
	}
	if err := a.persist(refreshed); err != nil {
		return "", err
	}
	return refreshed.AccessToken, nil
}

// requestToken exchanges a grant (authorization code or refresh token) for a
// token pair at the Spotify token endpoint.
func (a *Auth) requestToken(ctx context.Context, params url.Values) (*Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint,
		strings.NewReader(params.Encode()))
	if err != nil {
		return nil, fmt.Errorf("spotify: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(a.clientID, a.clientSecret)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("spotify: token request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var body struct {
			Error string `json:"error"`
			Desc  string `json:"error_description"`
		}
		json.NewDecoder(resp.Body).Decode(&body) //nolint:errcheck
		return nil, fmt.Errorf("spotify: token request %d: %s: %s",
			resp.StatusCode, body.Error, body.Desc)
	}

	var raw struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("spotify: decode token response: %w", err)
	}

	return &Token{
		AccessToken:  raw.AccessToken,
		RefreshToken: raw.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(raw.ExpiresIn) * time.Second),
	}, nil
}

// persist saves t to the store and updates the in-memory token.
func (a *Auth) persist(t *Token) error {
	if err := a.store.Save(t); err != nil {
		return err
	}
	a.mu.Lock()
	a.token = t
	a.mu.Unlock()
	slog.Info("spotify: token saved", "expires_at", t.ExpiresAt)
	return nil
}

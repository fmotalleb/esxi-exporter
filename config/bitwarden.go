package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/fmotalleb/esxi-exporter/internal/secure"
)

// BitwardenResolver fetches passwords from Bitwarden using the `bw serve`
// local HTTP bridge API. The operator must have `bw serve` running (e.g.
// `bw serve --port 8087`) with an unlocked vault.
//
// The resolver calls GET /object/item/{id} on the local bridge and
// extracts login.password from the response.
type BitwardenResolver struct {
	serverURL    string
	sessionToken string
	client       *http.Client
}

// NewBitwardenResolver creates a resolver that talks to a local `bw serve`
// instance. serverURL should be the base URL of the bridge API (e.g.
// "http://localhost:8087"). The session token is obtained by running
// `bw unlock` or from the BW_SESSION environment variable. When a
// non-empty token is passed it takes precedence; otherwise the resolver
// falls back to os.Getenv("BW_SESSION") on each call so that a session
// refresh does not require a process restart.
func NewBitwardenResolver(serverURL, sessionToken string) *BitwardenResolver {
	if serverURL == "" {
		serverURL = "http://localhost:8087"
	}
	return &BitwardenResolver{
		serverURL:    serverURL,
		sessionToken: sessionToken,
		client:       &http.Client{},
	}
}

// bwItem is the subset of the Bitwarden item JSON returned by bw serve.
type bwItem struct {
	Login *bwLogin `json:"login"`
}

type bwLogin struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// ResolvePassword fetches the Bitwarden item identified by
// host.BitwardenItemID from the local bw serve bridge and returns its
// login.password. Returns nil if the host has no item ID configured.
func (b *BitwardenResolver) ResolvePassword(ctx context.Context, host *ESXIHost) (*secure.SecureBytes, error) {
	if host.BitwardenItemID == "" {
		return nil, nil // not for us
	}

	// Resolve the session token: explicit config takes precedence,
	// otherwise read from BW_SESSION env var so a refresh doesn't need
	// a process restart.
	token := b.sessionToken
	if token == "" {
		token = os.Getenv("BW_SESSION")
	}
	if token == "" {
		return nil, fmt.Errorf("bitwarden: no session token available — run `bw unlock` and set BW_SESSION or configure session_token")
	}

	url := b.serverURL + "/object/item/" + host.BitwardenItemID
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("bitwarden: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bitwarden: get item %s: %w", host.BitwardenItemID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("bitwarden: get item %s: HTTP %d (%s)",
			host.BitwardenItemID, resp.StatusCode, string(body))
	}

	var item bwItem
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return nil, fmt.Errorf("bitwarden: decode item %s: %w", host.BitwardenItemID, err)
	}
	if item.Login == nil || item.Login.Password == "" {
		return nil, fmt.Errorf("bitwarden: item %s has no login.password", host.BitwardenItemID)
	}

	return secure.NewSecureBytes(item.Login.Password), nil
}

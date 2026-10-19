package client

import (
	"context"
	"net/http"

	"github.com/nulldrive/nulldrive/shared/api"
)

// End-to-end encryption endpoints (API v2, Phase 7). The client only moves
// the opaque key bundle; shared/e2ee creates and opens it.

// E2EEStatus returns the account's encryption state and key bundle.
func (c *Client) E2EEStatus(ctx context.Context) (api.E2EEStatus, error) {
	var out api.E2EEStatus
	return out, c.v2call(ctx, http.MethodGet, "/e2ee", nil, &out)
}

// EnableE2EE uploads a new key bundle (needs a session token).
func (c *Client) EnableE2EE(ctx context.Context, req api.E2EEEnableRequest) (api.E2EEStatus, error) {
	var out api.E2EEStatus
	return out, c.v2call(ctx, http.MethodPost, "/e2ee", req, &out)
}

// UpdateE2EEBundle replaces the key bundle (passphrase change, new
// recovery key).
func (c *Client) UpdateE2EEBundle(ctx context.Context, req api.E2EEBundleRequest) (api.E2EEStatus, error) {
	var out api.E2EEStatus
	return out, c.v2call(ctx, http.MethodPut, "/e2ee/bundle", req, &out)
}

// FinishE2EE ends the migration: the server deletes all plaintext.
func (c *Client) FinishE2EE(ctx context.Context, authKey string) (api.E2EEFinishResponse, error) {
	var out api.E2EEFinishResponse
	return out, c.v2call(ctx, http.MethodPost, "/e2ee/finish", api.E2EEFinishRequest{AuthKey: authKey}, &out)
}

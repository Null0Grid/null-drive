package client

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/Null0Grid/null-drive/shared/api"
)

// ClaimPairing exchanges a pairing code for a device token. It needs no
// credentials: the code is the credential.
func (c *Client) ClaimPairing(ctx context.Context, code, deviceName string) (api.ClaimPairingResponse, error) {
	var out api.ClaimPairingResponse
	return out, c.jsonCall(ctx, http.MethodPost, "/pairing/claim",
		api.ClaimPairingRequest{Code: code, DeviceName: deviceName}, &out)
}

// PairLinkScheme is the URL scheme in pairing QR codes and links:
//
//	nulldrive://pair?server=https%3A%2F%2Fnas.example.com&code=NULL-7K2Q-9F
const PairLinkScheme = "nulldrive"

// BuildPairLink returns the link encoded in the web UI's QR code.
func BuildPairLink(server, code string) string {
	q := url.Values{"server": {server}, "code": {code}}
	return PairLinkScheme + "://pair?" + q.Encode()
}

// ParsePairLink extracts server and code from a pairing link. ok is false
// if s is not a pairing link (e.g. it is a bare code).
func ParsePairLink(s string) (server, code string, ok bool, err error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(strings.ToLower(s), PairLinkScheme+"://") {
		return "", "", false, nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host != "pair" {
		return "", "", true, errors.New("that is not a valid Null Drive pairing link")
	}
	server, code = u.Query().Get("server"), u.Query().Get("code")
	if server == "" || code == "" {
		return "", "", true, errors.New("the pairing link is incomplete")
	}
	if server, err = NormalizeURL(server); err != nil {
		return "", "", true, err
	}
	return server, code, true, nil
}

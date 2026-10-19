// Package client is a Go client for the Null Drive v1 HTTP API.
//
// It only uses the standard library so the sync engine and the desktop app
// (and tests) can depend on it freely. The wire types come from shared/api,
// the same definitions the server uses.
package client

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Null0Grid/null-drive/shared/api"
)

// Error is a non-2xx API response.
type Error struct {
	Status  int
	Code    string
	Message string
	// Current is the server's version of the file on a 409 conflict.
	Current *api.File
}

func (e *Error) Error() string {
	return fmt.Sprintf("server: %s (%d %s)", e.Message, e.Status, e.Code)
}

// IsCode reports whether err is an API error with the given code
// (e.g. "conflict", "not_found", "precondition_required").
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// IsStatus reports whether err is an API error with the given HTTP status.
func IsStatus(err error, status int) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == status
}

// IsNetwork reports whether err means the server could not be reached (as
// opposed to the server answering with an error). Sync backs off on these.
func IsNetwork(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Status >= 500 || e.Status == http.StatusTooManyRequests
	}
	return err != nil && !errors.Is(err, context.Canceled)
}

// Client talks to one server with one bearer token (a device token for the
// sync client; a session token right after login).
type Client struct {
	base  string
	token string
	http  *http.Client
	// UserAgent identifies the client in server logs. The desktop app
	// sends "NullDrive/<version> (<os>/<arch>)", which the server shows
	// on the devices page.
	UserAgent string
	// TLS is used by the realtime WebSocket (the HTTP client carries its
	// own copy). Set by NewPinned.
	TLS *tls.Config

	// E2EE marks requests from a client that handles end-to-end encrypted
	// accounts (sets api.HeaderE2EE). Only the e2ee package's Remote sets
	// it: a client that can't decrypt must not receive ciphertext names.
	E2EE bool

	v2mu      sync.Mutex
	v2checked time.Time
	v2ok      bool
}

// New returns a client for baseURL (e.g. "http://nas:8080"). hc may be nil.
func New(baseURL, token string, hc *http.Client) *Client {
	if hc == nil {
		// No overall timeout: uploads and downloads can legitimately take
		// hours. Connection-level timeouts guard against dead servers.
		hc = &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			ResponseHeaderTimeout: 60 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   15 * time.Second,
		}}
	}
	return &Client{base: strings.TrimRight(baseURL, "/"), token: token, http: hc, UserAgent: "nulldrive-client"}
}

// NormalizeURL turns user input like "nas:8080" or "https://drive.example.com/"
// into a base URL, defaulting to http:// for LAN-style hosts.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("enter the server address")
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%q is not a valid server address", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// Token returns the bearer token in use.
func (c *Client) Token() string { return c.token }

// BaseURL returns the server base URL.
func (c *Client) BaseURL() string { return c.base }

// EncodePath percent-encodes each segment of a server path.
func EncodePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v1"+path, body)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("User-Agent", c.UserAgent)
	if c.E2EE {
		req.Header.Set(api.HeaderE2EE, "1")
	}
	return req, nil
}

func (c *Client) do(req *http.Request, out any) error {
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		return readError(res)
	}
	if out == nil || res.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, res.Body)
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func readError(res *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	var eb api.ErrorBody
	if json.Unmarshal(b, &eb) == nil && eb.Error.Code != "" {
		return &Error{Status: res.StatusCode, Code: eb.Error.Code, Message: eb.Error.Message, Current: eb.Error.Current}
	}
	msg := strings.TrimSpace(string(b))
	if msg == "" {
		msg = res.Status
	}
	return &Error{Status: res.StatusCode, Code: "http_" + strconv.Itoa(res.StatusCode), Message: msg}
}

func (c *Client) jsonCall(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := c.newRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.do(req, out)
}

func ifMatch(req *http.Request, v *int64) {
	if v != nil {
		req.Header.Set("If-Match", `"`+strconv.FormatInt(*v, 10)+`"`)
	}
}

// ---- system & auth ----

func (c *Client) Health(ctx context.Context) (api.HealthResponse, error) {
	var out api.HealthResponse
	return out, c.jsonCall(ctx, http.MethodGet, "/health", nil, &out)
}

func (c *Client) Login(ctx context.Context, username, password string) (api.SessionResponse, error) {
	return c.LoginTOTP(ctx, username, password, "")
}

// LoginTOTP logs in to an account with two-factor authentication. Without
// a code such an account answers 401 with code "totp_required".
func (c *Client) LoginTOTP(ctx context.Context, username, password, code string) (api.SessionResponse, error) {
	var out api.SessionResponse
	return out, c.jsonCall(ctx, http.MethodPost, "/auth/login", api.Credentials{Username: username, Password: password, TOTP: code}, &out)
}

func (c *Client) Logout(ctx context.Context) error {
	return c.jsonCall(ctx, http.MethodPost, "/auth/logout", nil, nil)
}

func (c *Client) Me(ctx context.Context) (api.MeResponse, error) {
	var out api.MeResponse
	return out, c.jsonCall(ctx, http.MethodGet, "/me", nil, &out)
}

// RegisterDevice needs a *session* token (log in first).
func (c *Client) RegisterDevice(ctx context.Context, name string) (api.CreateDeviceResponse, error) {
	var out api.CreateDeviceResponse
	return out, c.jsonCall(ctx, http.MethodPost, "/devices", api.CreateDeviceRequest{Name: name}, &out)
}

// ---- files ----

// ListFiles returns live entries (since == nil) or the change feed since a
// server version (tombstones included).
func (c *Client) ListFiles(ctx context.Context, since *int64) (api.FileList, error) {
	path := "/files"
	if since != nil {
		path += "?since=" + strconv.FormatInt(*since, 10)
	}
	var out api.FileList
	return out, c.jsonCall(ctx, http.MethodGet, path, nil, &out)
}

// Download streams a file's content. The caller must close the reader.
// sha is the server-reported SHA-256 of the content.
func (c *Client) Download(ctx context.Context, path string) (rc io.ReadCloser, sha string, err error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/files/"+EncodePath(path), nil)
	if err != nil {
		return nil, "", err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	if res.StatusCode != http.StatusOK {
		defer res.Body.Close()
		return nil, "", readError(res)
	}
	return res.Body, res.Header.Get(api.HeaderContentSHA256), nil
}

// Upload sends body as the content of path. sha (hex SHA-256) lets the
// server verify integrity and recognise idempotent retries. ifMatch is the
// version the client last saw (nil: none; pointer to 0: create only).
func (c *Client) Upload(ctx context.Context, path string, body io.Reader, size int64, sha string, ifMatchV *int64) (api.File, error) {
	req, err := c.newRequest(ctx, http.MethodPut, "/files/"+EncodePath(path), body)
	if err != nil {
		return api.File{}, err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")
	if sha != "" {
		req.Header.Set(api.HeaderContentSHA256, sha)
	}
	ifMatch(req, ifMatchV)
	var out api.File
	return out, c.do(req, &out)
}

func (c *Client) Delete(ctx context.Context, path string, ifMatchV *int64) (api.DeleteResponse, error) {
	req, err := c.newRequest(ctx, http.MethodDelete, "/files/"+EncodePath(path), nil)
	if err != nil {
		return api.DeleteResponse{}, err
	}
	ifMatch(req, ifMatchV)
	var out api.DeleteResponse
	return out, c.do(req, &out)
}

func (c *Client) Move(ctx context.Context, from, to string, ifMatchV *int64) (api.MoveResponse, error) {
	var out api.MoveResponse
	return out, c.jsonCall(ctx, http.MethodPost, "/files/move", api.MoveRequest{From: from, To: to, IfMatch: ifMatchV}, &out)
}

func (c *Client) CreateFolder(ctx context.Context, path string) (api.File, error) {
	var out api.File
	return out, c.jsonCall(ctx, http.MethodPost, "/folders", api.CreateFolderRequest{Path: path}, &out)
}

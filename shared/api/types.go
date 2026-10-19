// Package api holds the JSON wire types of the Null Drive v1 HTTP API.
// They live in shared/ so the server and the desktop client (Phase 4)
// compile against the same definitions and cannot drift apart.
package api

import (
	"encoding/json"
	"time"
)

// Token prefixes make credentials self-describing: the server knows which
// table to look in, and leaked tokens are easy to spot by secret scanners.
const (
	SessionTokenPrefix = "nds_"
	DeviceTokenPrefix  = "ndd_"
	SessionCookieName  = "nulldrive_session"
)

// Header names used by the files API.
const (
	HeaderContentSHA256 = "X-Content-SHA256"
	// HeaderMtime carries a file's modification time on the uploading
	// device (RFC 3339) on PUT /files/{path}; returned as "mtime".
	HeaderMtime = "X-NullDrive-Mtime"
	// HeaderFileID and HeaderSyncedID come with a download: the file's
	// stable id and (Markdown) Null Notes note id.
	HeaderFileID    = "X-NullDrive-File-Id"
	HeaderSyncedID  = "X-NullDrive-Synced-Id"
	HeaderRequestID = "X-Request-ID"
	// HeaderE2EE is sent by clients that handle end-to-end encrypted
	// accounts (value "1"). The server refuses file listings of an
	// encrypted account without it, so an older client fails with a clear
	// error instead of syncing ciphertext as files named "~…".
	HeaderE2EE = "X-NullDrive-E2EE"
	// HeaderCSRF carries the CSRF token on state-changing browser requests
	// (cookie-authenticated). Bearer-token clients don't need it.
	HeaderCSRF = "X-NullDrive-CSRF"
)

type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type SetupStatusResponse struct {
	SetupComplete bool `json:"setup_complete"`
}

type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// TOTP is the 6-digit authenticator code (or a recovery code) when
	// the account has two-factor authentication on.
	TOTP string `json:"totp,omitempty"`
}

type User struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	IsAdmin     bool      `json:"is_admin"`
	CreatedAt   time.Time `json:"created_at"`
	TOTPEnabled bool      `json:"totp_enabled"`
	// E2EE is the account's end-to-end encryption state: "off",
	// "migrating" (turned on, existing files still being encrypted) or "on".
	E2EE string `json:"e2ee"`
}

// SessionResponse is returned by setup and login.
type SessionResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      User      `json:"user"`
	// CSRFToken must be sent as X-NullDrive-CSRF on state-changing
	// requests that authenticate with the session cookie.
	CSRFToken string `json:"csrf_token"`
}

// MeResponse describes the authenticated caller. Device is set when the
// request used a device token.
type MeResponse struct {
	User   User    `json:"user"`
	Device *Device `json:"device,omitempty"`
	// CSRFToken is returned to session callers (the browser reloads the
	// page and asks again; the cookie itself is HttpOnly).
	CSRFToken string `json:"csrf_token,omitempty"`
}

type CreateDeviceRequest struct {
	Name string `json:"name"`
	// Scope "search" creates a token that can only call GET /search (for
	// Null Search). Empty or "full": a normal device/API token.
	Scope string `json:"scope,omitempty"`
}

type Device struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	// LastIP and ClientVersion are informational (shown on the devices
	// page); they are never used for authorization.
	LastIP        string `json:"last_ip,omitempty"`
	ClientVersion string `json:"client_version,omitempty"`
	// AddedVia is "password" or "pairing".
	AddedVia string `json:"added_via"`
	// Scope is "full" or "search" (GET /search only).
	Scope string `json:"scope"`
	// Online is true while the device holds a realtime connection.
	Online bool `json:"online"`
}

// CreateDeviceResponse carries the device token. It is only ever shown once;
// the server stores just its hash.
type CreateDeviceResponse struct {
	Device Device `json:"device"`
	Token  string `json:"token"`
}

type DeviceList struct {
	Devices []Device `json:"devices"`
}

// File is the metadata of one path: a file, or a folder when IsDir (folders
// have size 0 and an empty sha256). Deleted files are returned as
// tombstones (Deleted=true) by change feeds so clients can propagate deletes.
type File struct {
	// ID is the file's stable id: it survives renames, moves and restores
	// (search results and nulldrive://open links use it). Empty from
	// servers older than 0.8.
	ID         string     `json:"id,omitempty"`
	Path       string     `json:"path"`
	IsDir      bool       `json:"is_dir"`
	Size       int64      `json:"size"`
	SHA256     string     `json:"sha256"`
	Version    int64      `json:"version"`
	ModifiedAt time.Time  `json:"modified_at"`
	Deleted    bool       `json:"deleted"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
	// Mtime is the modification time the uploading device reported
	// (X-NullDrive-Mtime / CommitRequest.Mtime), millisecond precision;
	// absent if it sent none. ModifiedAt is the server's time of the change.
	Mtime *time.Time `json:"mtime,omitempty"`
	// SyncedID is the Null Notes note id from a Markdown file's
	// frontmatter (`id: <uuid>`), read by the server on upload.
	SyncedID string `json:"synced_id,omitempty"`
	// MovedTo is set on the tombstone a move leaves at the old path (in
	// ?since= change feeds): where the file went. That row is not a delete;
	// the file lives on at MovedTo with the same id.
	MovedTo string `json:"moved_to,omitempty"`
}

// FileList is the response of GET /files. Version is the server's current
// global change counter; a client stores it and passes it back as ?since=.
type FileList struct {
	Version int64  `json:"version"`
	Files   []File `json:"files"`
}

type CreateFolderRequest struct {
	Path string `json:"path"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// SystemInfo is shown by the setup wizard ("confirm storage location") and
// the settings page.
type SystemInfo struct {
	Version        string `json:"version"`
	DataDir        string `json:"data_dir"`
	DiskTotalBytes uint64 `json:"disk_total_bytes"`
	DiskFreeBytes  uint64 `json:"disk_free_bytes"`
	// DiskError is set when free space could not be determined.
	DiskError string `json:"disk_error,omitempty"`
}

type MoveRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
	// IfMatch optionally guards the move with the source's version (a
	// file's, or a folder's own row). The If-Match header works too.
	IfMatch *int64 `json:"if_match,omitempty"`
}

type MovedEntry struct {
	From string `json:"from"`
	File File   `json:"file"`
}

type MoveResponse struct {
	Version int64        `json:"version"`
	Moved   []MovedEntry `json:"moved"`
}

type DeleteResponse struct {
	Version int64  `json:"version"`
	Deleted []File `json:"deleted"`
}

// ErrorBody is the envelope for every non-2xx JSON response.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	// Current is set on 409 conflicts: the server's version of the file,
	// so a client can decide how to resolve without another round trip.
	Current *File `json:"current,omitempty"`
}

// ---- realtime (GET /api/v1/ws) ----

// Realtime message types (server -> client, JSON text frames).
const (
	// RealtimeHello is sent once on connect with the current version.
	RealtimeHello = "hello"
	// RealtimeChanged means the user's change feed reached Version.
	RealtimeChanged = "changed"
	// RealtimePresence (browser sessions only) reports a device going
	// online or offline.
	RealtimePresence = "presence"
)

type RealtimeMessage struct {
	Type     string `json:"type"`
	Version  int64  `json:"version,omitempty"`
	DeviceID int64  `json:"device_id,omitempty"`
	Online   *bool  `json:"online,omitempty"`
}

// ---- pairing ----

// PairingResponse is returned when a code is issued (Code is only present
// then) and when its status is polled.
type PairingResponse struct {
	ID        int64     `json:"id"`
	Code      string    `json:"code,omitempty"`
	Status    string    `json:"status"` // pending | claimed | expired | cancelled
	ExpiresAt time.Time `json:"expires_at"`
	Device    *Device   `json:"device,omitempty"`
}

// ClaimPairingRequest is sent by a new device: POST /pairing/claim.
type ClaimPairingRequest struct {
	Code       string `json:"code"`
	DeviceName string `json:"device_name"`
}

// ClaimPairingResponse carries the new device token (shown once) and the
// account it belongs to, so the client can show "Paired as alice".
type ClaimPairingResponse struct {
	Device Device `json:"device"`
	Token  string `json:"token"`
	User   User   `json:"user"`
	// E2EE is set for encrypted accounts: the *wrapped* key bundle, which
	// the new device unlocks with the passphrase or recovery key. The
	// pairing code itself is never used as a key (see DESIGN.md).
	E2EE *E2EEStatus `json:"e2ee,omitempty"`
}

// ---- server settings ----

// ServerSettings are the admin-editable runtime settings.
type ServerSettings struct {
	// ServerName is shown in LAN discovery ("Null Drive on <name>").
	ServerName string `json:"server_name"`
	// MDNSEnabled is the admin's choice; MDNSForcedOff is true when the
	// NULLDRIVE_MDNS=off environment variable overrides it.
	MDNSEnabled   bool `json:"mdns_enabled"`
	MDNSForcedOff bool `json:"mdns_forced_off"`
	// MDNSActive reports whether the advertiser is actually running and
	// MDNSAddrs the addresses it announces (for troubleshooting).
	MDNSActive bool     `json:"mdns_active"`
	MDNSAddrs  []string `json:"mdns_addrs,omitempty"`
	MDNSError  string   `json:"mdns_error,omitempty"`
	// Version history: keep every version for RetentionFullDays, then
	// one per day for RetentionDailyDays more (Phase 6).
	RetentionFullDays  int `json:"retention_full_days"`
	RetentionDailyDays int `json:"retention_daily_days"`
	// TrashDays: deleted files stay in Recently deleted, restorable, this
	// long (default 30), then are purged (Phase 6 of the search work).
	TrashDays int `json:"trash_days"`
}

type UpdateSettingsRequest struct {
	ServerName         *string `json:"server_name,omitempty"`
	MDNSEnabled        *bool   `json:"mdns_enabled,omitempty"`
	RetentionFullDays  *int    `json:"retention_full_days,omitempty"`
	RetentionDailyDays *int    `json:"retention_daily_days,omitempty"`
	TrashDays          *int    `json:"trash_days,omitempty"`
}

// ---- v2: chunks, manifests, versions (Phase 6) ----

// ChunkRef is one manifest entry: a chunk's SHA-256 (hex) and length.
type ChunkRef struct {
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

// ChunkCheckRequest: POST /api/v2/chunks/check.
type ChunkCheckRequest struct {
	Hashes []string `json:"hashes"`
}

// ChunkCheckResponse lists the hashes the client must upload.
type ChunkCheckResponse struct {
	Missing []string `json:"missing"`
}

// CommitRequest: PUT /api/v2/files/{path}. The whole-file SHA-256 is used
// for change detection and the v1 X-Content-SHA256 header; every chunk is
// verified by hash when uploaded.
type CommitRequest struct {
	Size   int64      `json:"size"`
	SHA256 string     `json:"sha256"`
	Chunks []ChunkRef `json:"chunks"`
	// Mtime: optional, the file's modification time on this device.
	Mtime *time.Time `json:"mtime,omitempty"`
}

// Manifest describes how a version is assembled from chunks.
type Manifest struct {
	Path    string     `json:"path"`
	Version int64      `json:"version"`
	Size    int64      `json:"size"`
	SHA256  string     `json:"sha256"`
	Chunks  []ChunkRef `json:"chunks"`
	// ID is the file's stable id (current version only); Mtime and
	// SyncedID as in File (SyncedID is never set on encrypted accounts).
	ID       string     `json:"id,omitempty"`
	Mtime    *time.Time `json:"mtime,omitempty"`
	SyncedID string     `json:"synced_id,omitempty"`
}

// FileVersion is one entry of a file's history.
type FileVersion struct {
	Version   int64     `json:"version"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	CreatedAt time.Time `json:"created_at"`
	// Device is the device's name; empty for the web app or an API session.
	Device   string `json:"device,omitempty"`
	DeviceID int64  `json:"device_id,omitempty"`
	// Origin is "upload", "restore" or "migrated" (stored before Phase 6).
	Origin  string `json:"origin"`
	Chunks  int    `json:"chunks"`
	Current bool   `json:"current"`
}

type VersionList struct {
	Path     string        `json:"path"`
	Deleted  bool          `json:"deleted"`
	Versions []FileVersion `json:"versions"`
}

// TrashItem is a deleted file that can still be restored.
type TrashItem struct {
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	DeletedAt time.Time `json:"deleted_at"`
	// Version is what a restore brings back (the last content).
	Version  int64 `json:"version"`
	Versions int   `json:"versions"`
	// PurgeAt is when it leaves the trash for good (deleted_at + trash_days).
	PurgeAt time.Time `json:"purge_at"`
}

type TrashList struct {
	Items []TrashItem `json:"items"`
}

// Stats is the storage dashboard. Server* fields are only filled for
// administrators.
type Stats struct {
	Files        int64 `json:"files"`
	LogicalBytes int64 `json:"logical_bytes"`
	// HistoryBytes counts every retained version at full size: what a
	// store without chunking and dedup would need.
	HistoryBytes int64 `json:"history_bytes"`
	Versions     int64 `json:"versions"`
	// StoredBytes: distinct chunks behind this user's versions.
	StoredBytes int64 `json:"stored_bytes"`
	TrashFiles  int64 `json:"trash_files"`

	ServerStoredBytes  int64 `json:"server_stored_bytes,omitempty"`
	ServerLogicalBytes int64 `json:"server_logical_bytes,omitempty"`
	ServerChunks       int64 `json:"server_chunks,omitempty"`
	ServerReclaimable  int64 `json:"server_reclaimable_bytes,omitempty"`
	RetentionFullDays  int   `json:"retention_full_days"`
	RetentionDailyDays int   `json:"retention_daily_days"`
	TrashDays          int   `json:"trash_days"`
}

// GCReport is the result of POST /api/v2/admin/gc.
type GCReport struct {
	VersionsPruned int   `json:"versions_pruned"`
	UploadsExpired int64 `json:"uploads_expired"`
	ChunksDeleted  int   `json:"chunks_deleted"`
	OrphansDeleted int   `json:"orphans_deleted"`
	BytesFreed     int64 `json:"bytes_freed"`
	DurationMS     int64 `json:"duration_ms"`
}

// ---- end-to-end encryption (Phase 7) ----

// E2EE states.
const (
	E2EEOff       = "off"
	E2EEMigrating = "migrating"
	E2EEOn        = "on"
)

// E2EEStatus: GET /api/v2/e2ee. Bundle is the client-produced key bundle
// (shared/e2ee.Bundle as JSON); the server stores it verbatim and cannot
// open it.
type E2EEStatus struct {
	State       string          `json:"state"`
	Bundle      json.RawMessage `json:"bundle,omitempty"`
	Fingerprint string          `json:"fingerprint,omitempty"`
	EnabledAt   *time.Time      `json:"enabled_at,omitempty"`
	// PlaintextFiles counts files and folders not encrypted yet (non-zero
	// only while migrating).
	PlaintextFiles int64 `json:"plaintext_files"`
}

// E2EEEnableRequest: POST /api/v2/e2ee. AuthKey proves possession of the
// master key in later bundle changes; the server keeps only its hash.
type E2EEEnableRequest struct {
	Bundle      json.RawMessage `json:"bundle"`
	Fingerprint string          `json:"fingerprint"`
	AuthKey     string          `json:"auth_key"`
}

// E2EEBundleRequest: PUT /api/v2/e2ee/bundle replaces the bundle (new
// passphrase or new recovery key). The fingerprint must not change.
type E2EEBundleRequest struct {
	AuthKey     string          `json:"auth_key"`
	Bundle      json.RawMessage `json:"bundle"`
	Fingerprint string          `json:"fingerprint"`
}

// E2EEFinishRequest: POST /api/v2/e2ee/finish, after every file has been
// re-uploaded encrypted. The server deletes all remaining plaintext.
type E2EEFinishRequest struct {
	AuthKey string `json:"auth_key"`
}

type E2EEFinishResponse struct {
	State         string `json:"state"`
	PurgedPaths   int64  `json:"purged_paths"`
	ChunksDeleted int    `json:"chunks_deleted"`
	BytesFreed    int64  `json:"bytes_freed"`
}

// ---- two-factor authentication (Phase 7) ----

// TOTPSetupResponse: POST /api/v1/auth/totp/setup. URI is an otpauth://
// URI for authenticator apps (shown as a QR code).
type TOTPSetupResponse struct {
	Secret string `json:"secret"`
	URI    string `json:"uri"`
}

// TOTPCodeRequest confirms setup with a code from the app.
type TOTPCodeRequest struct {
	Code string `json:"code"`
}

// TOTPEnableResponse returns one-time recovery codes (shown once).
type TOTPEnableResponse struct {
	RecoveryCodes []string `json:"recovery_codes"`
}

// TOTPDisableRequest needs the password and a current code (or a
// recovery code).
type TOTPDisableRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

// ---- audit log (Phase 7) ----

// AuditEvent is one security-relevant event.
type AuditEvent struct {
	ID       int64             `json:"id"`
	At       time.Time         `json:"at"`
	Event    string            `json:"event"`
	Username string            `json:"username,omitempty"`
	DeviceID int64             `json:"device_id,omitempty"`
	IP       string            `json:"ip,omitempty"`
	Detail   map[string]string `json:"detail,omitempty"`
}

// AuditList: GET /api/v1/admin/audit?before=<id>&limit=<n>. Next is the
// "before" value for the following page (absent on the last page).
type AuditList struct {
	Events []AuditEvent `json:"events"`
	Next   *int64       `json:"next,omitempty"`
}

// ---- cross-product search (Null franchise search contract v1) ----

// SearchResult is one hit of GET /search. The shape is shared by every Null
// product (null-grid/search-contract-v1.md); Null Drive sets Source
// "drive" and links to nulldrive://open?id=<id>.
type SearchResult struct {
	ID       string  `json:"id"`
	Source   string  `json:"source"`
	Title    string  `json:"title"`
	Snippet  string  `json:"snippet"`
	Path     string  `json:"path"`
	Open     string  `json:"open"`
	Modified string  `json:"modified"`
	SyncedID *string `json:"synced_id"`
}

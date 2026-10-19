package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/nulldrive/nulldrive/server/internal/meta"
	"github.com/nulldrive/nulldrive/shared/api"
)

const (
	settingServerName  = "server_name"
	settingMDNSEnabled = "mdns_enabled"
	maxServerName      = 63 // one DNS label: it becomes the mDNS instance name
)

func defaultServerName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "nulldrive"
	}
	h, _, _ = strings.Cut(h, ".")
	return h
}

func (s *Server) loadSettings(ctx context.Context) (api.ServerSettings, error) {
	out := api.ServerSettings{MDNSForcedOff: s.discoveryOff}
	name, err := s.store.Setting(ctx, settingServerName)
	if err != nil && !errors.Is(err, meta.ErrNotFound) {
		return out, err
	}
	if name == "" {
		name = defaultServerName()
	}
	out.ServerName = name
	en, err := s.store.Setting(ctx, settingMDNSEnabled)
	if err != nil && !errors.Is(err, meta.ErrNotFound) {
		return out, err
	}
	out.MDNSEnabled = en != "false"
	pol, err := s.store.Retention(ctx)
	if err != nil {
		return out, err
	}
	out.RetentionFullDays, out.RetentionDailyDays, out.TrashDays = pol.FullDays, pol.DailyDays, pol.TrashDays
	if s.discovery != nil {
		st := s.discovery.Status()
		out.MDNSActive, out.MDNSAddrs, out.MDNSError = st.Active, st.Addrs, st.Err
	}
	return out, nil
}

// RefreshDiscovery (re)applies the LAN advertisement from the current
// settings. Called at startup, after setup and after a settings change.
func (s *Server) RefreshDiscovery(ctx context.Context) {
	if s.discovery == nil {
		return
	}
	st, err := s.loadSettings(ctx)
	if err != nil {
		s.log.Warn("load settings for discovery", "err", err)
		return
	}
	done, err := s.store.SetupComplete(ctx)
	if err != nil {
		s.log.Warn("setup status for discovery", "err", err)
	}
	s.discovery.Apply(Advert{Enabled: st.MDNSEnabled && !s.discoveryOff, Name: st.ServerName, SetupDone: done})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.loadSettings(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleUpdateSettings: PATCH /api/v1/system/settings (admin session).
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if !principalFrom(r.Context()).User.IsAdmin {
		writeError(w, http.StatusForbidden, "admin_required", "only an administrator can change server settings")
		return
	}
	var req api.UpdateSettingsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.ServerName != nil {
		name := strings.TrimSpace(*req.ServerName)
		if name == "" || utf8.RuneCountInString(name) > maxServerName || strings.ContainsAny(name, ".\\\x00") {
			writeError(w, http.StatusBadRequest, "invalid_name", "server name must be 1-63 characters without dots or backslashes")
			return
		}
		if err := s.store.SetSetting(r.Context(), settingServerName, name); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	if req.MDNSEnabled != nil {
		v := "false"
		if *req.MDNSEnabled {
			v = "true"
		}
		if err := s.store.SetSetting(r.Context(), settingMDNSEnabled, v); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	if req.RetentionFullDays != nil || req.RetentionDailyDays != nil || req.TrashDays != nil {
		pol, err := s.store.Retention(r.Context())
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		if req.RetentionFullDays != nil {
			pol.FullDays = *req.RetentionFullDays
		}
		if req.RetentionDailyDays != nil {
			pol.DailyDays = *req.RetentionDailyDays
		}
		if req.TrashDays != nil {
			pol.TrashDays = *req.TrashDays
		}
		if err := s.store.SetRetention(r.Context(), pol); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_retention", err.Error())
			return
		}
	}
	var changed []string
	if req.ServerName != nil {
		changed = append(changed, "server_name")
	}
	if req.MDNSEnabled != nil {
		changed = append(changed, "mdns_enabled")
	}
	if req.RetentionFullDays != nil || req.RetentionDailyDays != nil {
		changed = append(changed, "retention")
	}
	if req.TrashDays != nil {
		changed = append(changed, "trash_days")
	}
	if len(changed) > 0 {
		u := principalFrom(r.Context()).User
		s.audit(r, meta.AuditSettingsChanged, &u, map[string]string{"changed": strings.Join(changed, ",")})
	}
	s.RefreshDiscovery(r.Context())
	s.handleGetSettings(w, r)
}

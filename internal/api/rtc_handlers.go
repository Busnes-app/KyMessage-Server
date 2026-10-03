package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/matrixrtc"
	"github.com/Busnes-app/ky_server_base/internal/synapseadmin"
)

const rtcPrefix = "/api/matrix/rtc"

func rtcPath(path string) bool { return path == rtcPrefix+"/sfu/get" || path == rtcPrefix+"/get_token" }

func (s *Server) rtcError(w http.ResponseWriter, status int, message string) {
	code := "M_UNKNOWN"
	switch status {
	case 400:
		code = "M_BAD_JSON"
	case 401:
		code = "M_UNKNOWN_TOKEN"
	case 403:
		code = "M_FORBIDDEN"
	case 404:
		code = "M_NOT_FOUND"
	case 429:
		code = "M_LIMIT_EXCEEDED"
	}
	s.writeJSON(w, status, map[string]string{"errcode": code, "error": message})
}

func (s *Server) handleRTCToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.rtc == nil || s.mas == nil {
		s.rtcError(w, 404, "Calling is not configured")
		return
	}
	if !s.allowClientAttempt("rtc:", r, 60, time.Minute) {
		s.rtcError(w, 429, "Too many call requests")
		return
	}
	if !jsonRequest(r) {
		s.rtcError(w, 400, "Expected JSON")
		return
	}
	var in struct {
		Room     string `json:"room"`
		RoomID   string `json:"room_id"`
		SlotID   string `json:"slot_id"`
		DeviceID string `json:"device_id"`
		OpenID   struct {
			AccessToken string `json:"access_token"`
			ServerName  string `json:"matrix_server_name"`
		} `json:"openid_token"`
		Member struct {
			ID       string `json:"id"`
			UserID   string `json:"claimed_user_id"`
			DeviceID string `json:"claimed_device_id"`
		} `json:"member"`
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&in); err != nil {
		s.rtcError(w, 400, "Invalid call request")
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		s.rtcError(w, 400, "Invalid call request")
		return
	}
	legacy := r.URL.Path == rtcPrefix+"/sfu/get"
	if legacy {
		in.RoomID = in.Room
		in.SlotID = "m.call#ROOM"
		in.Member.DeviceID = in.DeviceID
	}
	if !validRoomID(in.RoomID) || !rtcValue(in.SlotID) || !rtcValue(in.Member.DeviceID) || (!legacy && !rtcValue(in.Member.ID)) {
		s.rtcError(w, 400, "Invalid room, slot or device")
		return
	}
	// The caller cannot choose a network destination, even when presenting a foreign OpenID token.
	if in.OpenID.ServerName != s.config.Matrix.ServerName || !rtcValue(in.OpenID.AccessToken) {
		s.rtcError(w, 403, "Only this deployment's users may call")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", s.matrixTargets.Synapse+"/_matrix/federation/v1/openid/userinfo?access_token="+url.QueryEscape(in.OpenID.AccessToken), nil)
	resp, err := s.probeHTTP.Do(req)
	if err != nil {
		s.rtcError(w, 502, "Unable to verify identity")
		return
	}
	defer resp.Body.Close()
	var user struct {
		Sub string `json:"sub"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&user) != nil || !strings.HasPrefix(user.Sub, "@") || !strings.HasSuffix(user.Sub, ":"+s.config.Matrix.ServerName) {
		s.rtcError(w, 401, "Invalid Matrix OpenID token")
		return
	}
	if !legacy && in.Member.UserID != user.Sub {
		s.rtcError(w, 403, "Call identity does not match")
		return
	}
	active, err := s.activeCallUsers(ctx)
	if err != nil {
		s.rtcError(w, 502, "Unable to verify access")
		return
	}
	if !active[user.Sub] {
		s.rtcError(w, 403, "Chat access is disabled")
		return
	}
	var joined bool
	err = s.mas.AsConsole(ctx, func(ctx context.Context, token string) error {
		members, err := s.rooms.Members(ctx, token, in.RoomID)
		if err != nil {
			return err
		}
		joined = slices.Contains(members, user.Sub)
		return nil
	})
	if err != nil {
		s.rtcError(w, 502, "Unable to verify room membership")
		return
	}
	if !joined {
		s.rtcError(w, 403, "Join the Matrix room before its call")
		return
	}
	room := matrixrtc.Hash(in.RoomID, in.SlotID)
	identity := matrixrtc.Hash(user.Sub, in.Member.DeviceID, in.Member.ID)
	if legacy {
		identity = user.Sub + ":" + in.DeviceID
	}
	if err := s.rtc.Create(ctx, room, in.RoomID); err != nil {
		s.rtcError(w, 502, "Call service is unavailable")
		return
	}
	s.writeJSON(w, 200, map[string]string{"url": "wss" + strings.TrimPrefix(s.config.Matrix.RTCHost, "https"), "jwt": s.rtc.Join(room, identity, user.Sub)})
}

func rtcValue(v string) bool {
	return len(v) > 0 && len(v) <= 1024 && !strings.ContainsFunc(v, func(r rune) bool { return r < 32 || r == 127 })
}

// No access cache: a directory webhook denies a new grant even before the MAS sweep locks it.
func (s *Server) activeCallUsers(ctx context.Context) (map[string]bool, error) {
	users, err := s.mas.Users(ctx)
	if err != nil {
		return nil, err
	}
	statuses, err := s.store.Users().DirectoryStatuses(ctx, "kyidentity")
	if err != nil {
		return nil, err
	}
	active := map[string]bool{}
	for _, u := range users {
		if u.Subject != "" && !u.Ambiguous && !u.Locked && !u.Deactivated && statuses[u.Subject] == "active" {
			active["@"+u.Username+":"+s.config.Matrix.ServerName] = true
		}
	}
	return active, nil
}

// ReconcileCalls evicts offboarded users and people removed from a Matrix room. Metadata in
// grants and rooms comes only from this server and cannot be changed by participants.
func (s *Server) ReconcileCalls(ctx context.Context) error {
	if s.rtc == nil || s.mas == nil {
		return nil
	}
	rooms, err := s.rtc.Rooms(ctx)
	if err != nil {
		return err
	}
	if len(rooms) == 0 {
		return nil
	}
	active, err := s.activeCallUsers(ctx)
	if err != nil {
		return err
	}
	var result error
	for _, room := range rooms {
		if !validRoomID(room.Metadata) {
			return errors.New("LiveKit room has invalid Matrix metadata")
		}
		participants, err := s.rtc.Participants(ctx, room.Name)
		if err != nil {
			return err
		}
		var members []string
		err = s.mas.AsConsole(ctx, func(ctx context.Context, token string) error {
			var err error
			members, err = s.rooms.Members(ctx, token, room.Metadata)
			return err
		})
		// A deleted room has no members; other failures must not be interpreted as successful reads.
		if err != nil {
			var se *synapseadmin.Error
			if !errors.As(err, &se) || se.Status != http.StatusNotFound {
				return err
			}
			members = nil
		}
		for _, p := range participants {
			if active[p.Metadata] && slices.Contains(members, p.Metadata) {
				continue
			}
			if err := s.rtc.Remove(ctx, room.Name, p.Identity); err != nil {
				result = errors.Join(result, err)
				continue
			}
			log.Printf("[MATRIX RTC] removed participant from call")
		}
	}
	return result
}

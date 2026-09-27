package api

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/google/uuid"
)

const messagingDeviceHeader = "X-KyMessages-Device"

func (s *Server) messagingRoutes() {
	s.mux.HandleFunc("POST /api/messaging/devices/{device}/recovery-auth", s.requireMessaging(s.handleMessagingRecoveryAuth))
	s.mux.HandleFunc("GET /api/messaging/recovery-auth/callback", s.requireMessaging(s.handleMessagingRecoveryAuthCallback))
	s.mux.HandleFunc("POST /api/messaging/devices/key-packages", s.requireMessaging(s.handleMessagingPublishKeyPackage))
	s.mux.HandleFunc("POST /api/messaging/rooms/{room}/key-packages/claim", s.requireMessaging(s.handleMessagingClaimKeyPackage))
	s.mux.HandleFunc("GET /api/messaging/rooms/{room}/delivery", s.requireMessaging(s.handleMessagingDelivery))
	s.mux.HandleFunc("POST /api/messaging/rooms/{room}/events", s.requireMessaging(s.handleMessagingAppend))
	s.mux.HandleFunc("GET /api/messaging/rooms/{room}/events", s.requireMessaging(s.handleMessagingEvents))
	s.mux.HandleFunc("POST /api/messaging/devices", s.requireMessaging(s.handleMessagingEnroll))
	s.mux.HandleFunc("GET /api/messaging/devices", s.requireMessaging(s.handleMessagingDevices))
	s.mux.HandleFunc("POST /api/messaging/devices/{device}/verify", s.requireMessaging(s.handleMessagingVerify))
	s.mux.HandleFunc("POST /api/messaging/devices/{device}/approve", s.requireMessaging(s.handleMessagingApprove))
	s.mux.HandleFunc("DELETE /api/messaging/devices/{device}", s.requireMessaging(s.handleMessagingRevoke))
	s.mux.HandleFunc("POST /api/messaging/rooms", s.requireMessaging(s.handleMessagingCreateRoom))
	s.mux.HandleFunc("GET /api/messaging/rooms", s.requireMessaging(s.handleMessagingRooms))
	s.mux.HandleFunc("GET /api/messaging/rooms/{room}/members", s.requireMessaging(s.handleMessagingMembers))
	s.mux.HandleFunc("POST /api/messaging/rooms/{room}/members", s.requireMessaging(s.handleMessagingInvite))
	s.mux.HandleFunc("POST /api/messaging/rooms/{room}/join", s.requireMessaging(s.handleMessagingJoin))
	s.mux.HandleFunc("DELETE /api/messaging/rooms/{room}/members/{user}", s.requireMessaging(s.handleMessagingRemove))
	// Avoid returning the SPA for unknown messaging APIs or unsupported methods.
	s.mux.HandleFunc("/api/messaging/", func(w http.ResponseWriter, r *http.Request) {
		s.writeError(w, http.StatusNotFound, "Unknown messaging endpoint")
	})
}

type messagingHandler func(http.ResponseWriter, *http.Request, store.MessagingActor)

func (s *Server) requireMessaging(next messagingHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		user, session, err := s.sessions.AuthenticateRequest(r)
		if err != nil {
			if errors.Is(err, auth.ErrPasswordChangeRequired) {
				s.writeError(w, http.StatusForbidden, "Password replacement required")
			} else {
				s.writeError(w, http.StatusUnauthorized, "Authentication required")
			}
			return
		}
		// kysignon is the existing persisted provider name for suite OIDC. Local
		// bootstrap administrators cannot enroll or approve a decryption device.
		if user.SSOProvider != "kysignon" || user.SSOSubject == "" || user.PasswordHash != "" {
			s.writeError(w, http.StatusForbidden, "Suite OIDC sign-in required")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, s.config.Server.AppURL) {
			s.writeError(w, http.StatusForbidden, "Origin not allowed")
			return
		}
		if !s.allowAttempt("messaging:"+user.ID, 120, time.Minute) {
			s.writeError(w, http.StatusTooManyRequests, "Too many messaging requests")
			return
		}
		actor := store.MessagingActor{UserID: user.ID, SessionHash: session.TokenHash, IP: s.requestIP(r)}
		if credential := r.Header.Get(messagingDeviceHeader); credential != "" {
			if !messagingHex(credential) {
				s.writeError(w, http.StatusForbidden, "Invalid device credential")
				return
			}
			actor.DeviceTokenHash = crypto.SHA256Hex([]byte(credential))
		}
		next(w, r, actor)
	}
}

func messagingHex(value string) bool {
	bytes, err := hex.DecodeString(value)
	return err == nil && len(bytes) == 32 && hex.EncodeToString(bytes) == value
}

func messagingName(value string) bool {
	return utf8.ValidString(value) && len(value) > 0 && len(value) <= 80 && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

func (s *Server) messagingJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	return s.messagingJSONLimit(w, r, target, 8192)
}

func (s *Server) messagingJSONLimit(w http.ResponseWriter, r *http.Request, target any, limit int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid request body")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		s.writeError(w, http.StatusBadRequest, "Expected one JSON object")
		return false
	}
	return true
}

func (s *Server) messagingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.writeError(w, http.StatusNotFound, "Messaging resource not found")
	case errors.Is(err, store.ErrMessagingDenied):
		s.writeError(w, http.StatusForbidden, "Messaging access denied")
	case errors.Is(err, store.ErrMessagingConflict):
		s.writeError(w, http.StatusConflict, "Messaging state conflict")
	case errors.Is(err, store.ErrMessagingLimit):
		s.writeError(w, http.StatusConflict, "Messaging capacity reached")
	default:
		s.writeError(w, http.StatusInternalServerError, "Messaging operation failed")
	}
}

func deviceView(d store.MessagingDevice) map[string]any {
	key, _ := base64.StdEncoding.DecodeString(d.PublicKey)
	fingerprint := sha256.Sum256(key)
	return map[string]any{"id": d.ID, "user_id": d.UserID, "name": d.Name, "public_key": d.PublicKey, "fingerprint": hex.EncodeToString(fingerprint[:]), "status": d.Status, "approved_by": d.ApprovedBy, "created_at": d.CreatedAt}
}

func (s *Server) handleMessagingEnroll(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	var request struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
		TokenHash string `json:"token_hash"`
	}
	if !s.messagingJSON(w, r, &request) {
		return
	}
	key, err := base64.StdEncoding.DecodeString(request.PublicKey)
	if err != nil || len(key) != ed25519.PublicKeySize || base64.StdEncoding.EncodeToString(key) != request.PublicKey || !messagingName(request.Name) || !messagingHex(request.TokenHash) {
		s.writeError(w, http.StatusBadRequest, "Valid name, Ed25519 public key and SHA-256 token hash required")
		return
	}
	if !s.allowAttempt("messaging-enroll:"+actor.UserID, 10, 5*time.Minute) {
		s.writeError(w, http.StatusTooManyRequests, "Too many device enrollments")
		return
	}
	enrollment := store.MessagingEnrollment{
		Device:    store.MessagingDevice{ID: uuid.NewString(), UserID: actor.UserID, Name: request.Name, PublicKey: request.PublicKey, Status: "unverified", CreatedAt: time.Now().Unix()},
		ExpiresAt: time.Now().Add(5 * time.Minute).Unix(), TokenHash: request.TokenHash,
	}
	// Return the exact bytes; clients sign those, not a reserialized JSON object.
	challenge, err := json.Marshal(struct {
		Domain, Origin, UserID, DeviceID, PublicKey, TokenHash, Nonce string
		ExpiresAt                                                     int64
	}{"KyMessages enrollment v1", s.config.Server.AppURL, actor.UserID, enrollment.Device.ID, request.PublicKey, request.TokenHash, crypto.RandomHex(32), enrollment.ExpiresAt})
	if err != nil {
		s.messagingError(w, err)
		return
	}
	enrollment.Challenge = string(challenge)
	if err := s.store.Messaging().EnrollDevice(r.Context(), actor, enrollment); err != nil {
		s.messagingError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"device": deviceView(enrollment.Device), "signing_input": base64.StdEncoding.EncodeToString(challenge), "expires_at": enrollment.ExpiresAt})
}

func (s *Server) handleMessagingVerify(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	var request struct {
		Signature string `json:"signature"`
	}
	if !s.messagingJSON(w, r, &request) {
		return
	}
	signature, err := base64.StdEncoding.DecodeString(request.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		s.writeError(w, http.StatusBadRequest, "Valid Ed25519 signature required")
		return
	}
	device, err := s.store.Messaging().VerifyDevice(r.Context(), actor, r.PathValue("device"), signature)
	if err != nil {
		s.messagingError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"device": deviceView(*device)})
}

func (s *Server) handleMessagingDevices(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	devices, err := s.store.Messaging().ListDevices(r.Context(), actor)
	if err != nil {
		s.messagingError(w, err)
		return
	}
	views := make([]map[string]any, 0, len(devices))
	for _, device := range devices {
		views = append(views, deviceView(device))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"devices": views})
}

func (s *Server) handleMessagingApprove(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	if err := s.store.Messaging().ApproveDevice(r.Context(), actor, r.PathValue("device")); err != nil {
		s.messagingError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]bool{"approved": true})
}

func (s *Server) handleMessagingRevoke(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	if err := s.store.Messaging().RevokeDevice(r.Context(), actor, r.PathValue("device")); err != nil {
		s.messagingError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]bool{"revoked": true})
}

func (s *Server) handleMessagingCreateRoom(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	var request struct {
		Name string `json:"name"`
	}
	if !s.messagingJSON(w, r, &request) {
		return
	}
	if !messagingName(request.Name) {
		s.writeError(w, http.StatusBadRequest, "Valid room name required")
		return
	}
	room := store.MessagingRoom{ID: uuid.NewString(), Name: request.Name, OwnerID: actor.UserID, CreatedAt: time.Now().Unix(), Membership: "active"}
	if err := s.store.Messaging().CreateRoom(r.Context(), actor, room); err != nil {
		s.messagingError(w, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, roomView(room))
}

func roomView(room store.MessagingRoom) map[string]any {
	return map[string]any{"id": room.ID, "name": room.Name, "owner_id": room.OwnerID, "created_at": room.CreatedAt, "membership": room.Membership}
}

func (s *Server) handleMessagingRooms(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		var err error
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > 1_000_000 {
			s.writeError(w, http.StatusBadRequest, "Invalid offset")
			return
		}
	}
	rooms, err := s.store.Messaging().ListRooms(r.Context(), actor, offset)
	if err != nil {
		s.messagingError(w, err)
		return
	}
	views := make([]map[string]any, 0, len(rooms))
	for _, room := range rooms {
		views = append(views, roomView(room))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"rooms": views})
}

func (s *Server) handleMessagingMembers(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	members, err := s.store.Messaging().ListMembers(r.Context(), actor, r.PathValue("room"))
	if err != nil {
		s.messagingError(w, err)
		return
	}
	views := make([]map[string]string, 0, len(members))
	for _, member := range members {
		views = append(views, map[string]string{"user_id": member.UserID, "status": member.Status})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"members": views})
}

func (s *Server) handleMessagingInvite(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	var request struct {
		UserID string `json:"user_id"`
	}
	if !s.messagingJSON(w, r, &request) {
		return
	}
	if len(request.UserID) == 0 || len(request.UserID) > 64 || strings.ContainsFunc(request.UserID, unicode.IsControl) {
		s.writeError(w, http.StatusBadRequest, "Valid user ID required")
		return
	}
	if err := s.store.Messaging().InviteMember(r.Context(), actor, r.PathValue("room"), request.UserID); err != nil {
		s.messagingError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]bool{"invited": true})
}

func (s *Server) handleMessagingJoin(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	if err := s.store.Messaging().AcceptInvite(r.Context(), actor, r.PathValue("room")); err != nil {
		s.messagingError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]bool{"joined": true})
}

func (s *Server) handleMessagingRemove(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	if err := s.store.Messaging().RemoveMember(r.Context(), actor, r.PathValue("room"), r.PathValue("user")); err != nil {
		s.messagingError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
}

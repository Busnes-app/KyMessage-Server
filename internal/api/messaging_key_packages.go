package api

import (
	"encoding/base64"
	"net/http"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/crypto"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/google/uuid"
)

func (s *Server) handleMessagingPublishKeyPackage(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	var input struct {
		Payload   string `json:"payload"`
		RoomID    string `json:"room_id"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if !s.messagingJSONLimit(w, r, &input, 24*1024) {
		return
	}
	if input.RoomID != "" {
		id, err := uuid.Parse(input.RoomID)
		if err != nil || id.String() != input.RoomID {
			s.writeError(w, 400, "Canonical room UUID required")
			return
		}
	}
	wire, err := base64.StdEncoding.DecodeString(input.Payload)
	now := time.Now().Unix()
	if err != nil || len(wire) == 0 || len(wire) > 16*1024 || base64.StdEncoding.EncodeToString(wire) != input.Payload || input.ExpiresAt <= now || input.ExpiresAt > now+7*24*60*60 {
		s.writeError(w, 400, "Canonical base64 KeyPackage up to 16 KiB and expiry within seven days required")
		return
	}
	kp := store.MessagingKeyPackage{ID: crypto.SHA256Hex(wire), Payload: input.Payload, ExpiresAt: input.ExpiresAt, RoomID: input.RoomID}
	if err := s.store.Messaging().PublishKeyPackage(r.Context(), actor, kp); err != nil {
		s.messagingError(w, err)
		return
	}
	s.writeJSON(w, 200, map[string]any{"package_id": kp.ID, "expires_at": kp.ExpiresAt})
}

func (s *Server) handleMessagingClaimKeyPackage(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	var input struct {
		DeviceID  string `json:"device_id"`
		RequestID string `json:"request_id"`
	}
	if !s.messagingJSON(w, r, &input) {
		return
	}
	for _, value := range []string{input.DeviceID, input.RequestID} {
		id, err := uuid.Parse(value)
		if err != nil || id.String() != value {
			s.writeError(w, 400, "Canonical device and request UUIDs required")
			return
		}
	}
	kp, err := s.store.Messaging().ClaimKeyPackage(r.Context(), actor, r.PathValue("room"), input.DeviceID, input.RequestID)
	if err != nil {
		s.messagingError(w, err)
		return
	}
	s.writeJSON(w, 200, map[string]any{"package_id": kp.ID, "device_id": kp.DeviceID, "payload": kp.Payload, "expires_at": kp.ExpiresAt})
}

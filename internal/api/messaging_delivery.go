package api

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/google/uuid"
)

func (s *Server) handleMessagingDelivery(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	state, err := s.store.Messaging().DeliveryState(r.Context(), actor, r.PathValue("room"))
	if err != nil {
		s.messagingError(w, err)
		return
	}
	devices := make([]map[string]any, 0, len(state.Devices))
	for _, d := range state.Devices {
		devices = append(devices, map[string]any{"id": d.ID, "user_id": d.UserID, "public_key": d.PublicKey, "generation": d.Generation, "identity_generation": d.IdentityGeneration})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"epoch": state.Epoch, "sequence": state.Sequence, "roster_hash": state.RosterHash, "paused": state.Paused, "devices": devices, "retention_days": state.RetentionDays, "retained_from": state.RetainedFrom})
}

func messagingWirePayload(value string) bool {
	if len(value) == 0 || len(value) > base64.StdEncoding.EncodedLen(64*1024) {
		return false
	}
	data, err := base64.StdEncoding.DecodeString(value)
	return err == nil && len(data) > 0 && len(data) <= 64*1024 && base64.StdEncoding.EncodeToString(data) == value
}

// messagingDailyEventLimit bounds appends per account per day.
const messagingDailyEventLimit = 5000

func (s *Server) handleMessagingAppend(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	var request struct {
		ID         string            `json:"id"`
		Kind       string            `json:"kind"`
		Epoch      *int64            `json:"epoch"`
		RosterHash string            `json:"roster_hash"`
		Payload    string            `json:"payload"`
		Welcomes   map[string]string `json:"welcomes"`
	}
	if !s.messagingJSONLimit(w, r, &request, 768*1024) {
		return
	}
	id, err := uuid.Parse(request.ID)
	if err != nil || id.String() != request.ID || request.Epoch == nil || *request.Epoch < 0 || (request.Kind != "application" && request.Kind != "commit") || !messagingHex(request.RosterHash) || !messagingWirePayload(request.Payload) {
		s.writeError(w, http.StatusBadRequest, "Valid event ID, kind, epoch, roster hash and base64 payload required")
		return
	}
	if request.Welcomes == nil {
		request.Welcomes = map[string]string{}
	}
	size := len(request.Payload)
	for device, payload := range request.Welcomes {
		id, err := uuid.Parse(device)
		if err != nil || id.String() != device || !messagingWirePayload(payload) {
			s.writeError(w, http.StatusBadRequest, "Invalid device Welcome")
			return
		}
		size += len(payload)
	}
	if size > 512*1024 || (request.Kind == "application" && len(request.Welcomes) != 0) {
		s.writeError(w, http.StatusBadRequest, "Event payload limit or kind violation")
		return
	}
	// Event metadata, receipts and audit rows outlive ciphertext retention and fill the backup
	// capsule. A daily cap keeps one account from doing that in hours.
	if !s.allowAccountAttempt("messaging:daily-events:"+actor.UserID, messagingDailyEventLimit, 24*time.Hour) {
		s.writeError(w, http.StatusTooManyRequests, "Daily message limit reached for this account")
		return
	}
	receipt, err := s.store.Messaging().AppendEvent(r.Context(), actor, r.PathValue("room"), store.MessagingEventInput{ID: request.ID, Kind: request.Kind, Epoch: *request.Epoch, RosterHash: request.RosterHash, Payload: request.Payload, Welcomes: request.Welcomes})
	if err != nil {
		s.messagingError(w, err)
		return
	}
	s.wakeMessaging(r.PathValue("room"))
	// A new append and its exact retry return the same durable acknowledgement.
	s.writeJSON(w, http.StatusOK, map[string]any{"sequence": receipt.Sequence, "epoch": receipt.Epoch})
}

func (s *Server) handleMessagingEvents(w http.ResponseWriter, r *http.Request, actor store.MessagingActor) {
	after := int64(0)
	if raw := r.URL.Query().Get("after"); raw != "" {
		var err error
		after, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || after < 0 {
			s.writeError(w, http.StatusBadRequest, "Invalid event cursor")
			return
		}
	}
	page, err := s.store.Messaging().ReadEvents(r.Context(), actor, r.PathValue("room"), after)
	if err != nil {
		s.messagingError(w, err)
		return
	}
	events := make([]map[string]any, 0, len(page.Events))
	for _, e := range page.Events {
		events = append(events, map[string]any{"id": e.ID, "device_id": e.DeviceID, "sequence": e.Sequence, "epoch": e.Epoch, "kind": e.Kind, "roster_hash": e.RosterHash, "payload": e.Payload, "welcome": e.Welcome, "created_at": e.CreatedAt, "expires_at": e.ExpiresAt})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"events": events, "next": page.Next, "start_sequence": page.StartSequence})
}

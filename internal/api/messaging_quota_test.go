package api_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/google/uuid"
)

// The HTTP layer capped the declared epoch at 4096 while the store let rooms advance past it,
// so a room reaching epoch 4097 accepted no further event. The store's current-epoch check is
// the only bound.
func TestMessagingEpochHasNoHTTPCeiling(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	session := messagingLogin(t, st, "alice")
	d := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
	w := messagingRequest(t, srv, "POST", "/api/messaging/rooms", session, d.Token, map[string]string{"name": "Long lived"})
	messagingCode(t, w, 201)
	var room struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	path := "/api/messaging/rooms/" + room.ID
	w = messagingRequest(t, srv, "GET", path+"/delivery", session, d.Token, nil)
	var state struct {
		RosterHash string `json:"roster_hash"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"id": uuid.NewString(), "kind": "commit", "epoch": 4097, "roster_hash": state.RosterHash, "payload": "b3BhcXVl"}
	// Reaches the store, which refuses the stale epoch as a conflict rather than malformed input.
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/events", session, d.Token, body), 409)
}

// Event metadata, receipts and audit rows outlive ciphertext and fill the backup capsule, so
// one account cannot append without bound.
func TestMessagingDailyEventCap(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	session := messagingLogin(t, st, "alice")
	d := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
	w := messagingRequest(t, srv, "POST", "/api/messaging/rooms", session, d.Token, map[string]string{"name": "Busy"})
	messagingCode(t, w, 201)
	var room struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	for range api.MessagingDailyEventLimitForTest {
		api.AllowAccountAttemptForTest(srv, "messaging:daily-events:alice", api.MessagingDailyEventLimitForTest, 24*time.Hour)
	}
	body := map[string]any{"id": uuid.NewString(), "kind": "commit", "epoch": 0, "roster_hash": strings.Repeat("0", 64), "payload": "b3BhcXVl"}
	messagingCode(t, messagingRequest(t, srv, "POST", "/api/messaging/rooms/"+room.ID+"/events", session, d.Token, body), 429)
}

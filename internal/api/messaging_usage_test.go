package api_test

import (
	"encoding/json"
	"testing"
)

func TestMessagingUsageDTOIsAdminMetadata(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	session := messagingLogin(t, st, "usage-member")
	d := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
	messagingCode(t, messagingRequest(t, srv, "POST", "/api/messaging/rooms", session, d.Token, map[string]string{"name": "Visible room metadata"}), 201)
	admin := loginAs(t, srv, st, "usage-admin", "admin")
	response := do(t, srv, "GET", "/api/admin/messaging/usage", admin)
	messagingCode(t, response, 200)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("usage can be cached")
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if len(value) != 5 || string(value["room_count"]) != "1" {
		t.Fatal(response.Body.String())
	}
	var rooms []map[string]json.RawMessage
	if err := json.Unmarshal(value["rooms"], &rooms); err != nil {
		t.Fatal(err)
	}
	if len(rooms) != 1 || len(rooms[0]) != 6 || string(rooms[0]["name"]) != `"Visible room metadata"` {
		t.Fatal(response.Body.String())
	}
	for _, field := range []string{"active_events", "retained_bytes", "receipts"} {
		if string(rooms[0][field]) != "0" {
			t.Fatal(response.Body.String())
		}
	}
}

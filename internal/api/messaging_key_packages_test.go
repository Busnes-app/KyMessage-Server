package api_test

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMessagingKeyPackageHTTP(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	session := messagingLogin(t, st, "alice")
	device := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
	endpoint := "/api/messaging/devices/key-packages"
	body := map[string]any{"payload": "b3BhcXVl", "expires_at": time.Now().Add(time.Hour).Unix()}
	messagingCode(t, messagingRequest(t, srv, "POST", endpoint, "", "", body), 401)
	messagingCode(t, messagingRequest(t, srv, "POST", endpoint, session, "", body), 403)
	for _, bad := range []map[string]any{
		{"payload": "!", "expires_at": time.Now().Add(time.Hour).Unix()},
		{"payload": "", "expires_at": time.Now().Add(time.Hour).Unix()},
		{"payload": base64.StdEncoding.EncodeToString(make([]byte, 16385)), "expires_at": time.Now().Add(time.Hour).Unix()},
		{"payload": "b3BhcXVl", "expires_at": time.Now().Add(-time.Hour).Unix()},
		{"payload": "b3BhcXVl", "expires_at": time.Now().Add(8 * 24 * time.Hour).Unix()},
		{"payload": "b3BhcXVl", "expires_at": time.Now().Add(time.Hour).Unix(), "device_id": uuid.NewString()},
	} {
		messagingCode(t, messagingRequest(t, srv, "POST", endpoint, session, device.Token, bad), 400)
	}
	first := messagingRequest(t, srv, "POST", endpoint, session, device.Token, body)
	messagingCode(t, first, 200)
	retry := messagingRequest(t, srv, "POST", endpoint, session, device.Token, body)
	messagingCode(t, retry, 200)
	if first.Body.String() != retry.Body.String() {
		t.Fatal("publication retry differs")
	}
	other := messagingLogin(t, st, "bob")
	od := verifyEnrollment(t, srv, other, requestEnrollment(t, srv, other))
	created := messagingRequest(t, srv, "POST", "/api/messaging/rooms", other, od.Token, map[string]string{"name": "Room"})
	messagingCode(t, created, 201)
	var room struct{ ID string }
	if err := json.Unmarshal(created.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	path := "/api/messaging/rooms/" + room.ID
	claim := map[string]string{"device_id": device.ID, "request_id": uuid.NewString()}
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/key-packages/claim", other, od.Token, claim), 404)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/members", other, od.Token, map[string]string{"user_id": "alice"}), 200)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/join", session, device.Token, nil), 200)
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/key-packages/claim", other, od.Token, map[string]string{"device_id": device.ID, "request_id": "bad"}), 400)
	got := messagingRequest(t, srv, "POST", path+"/key-packages/claim", other, od.Token, claim)
	messagingCode(t, got, 200)
	again := messagingRequest(t, srv, "POST", path+"/key-packages/claim", other, od.Token, claim)
	messagingCode(t, again, 200)
	if got.Body.String() != again.Body.String() {
		t.Fatal("claim retry differs")
	}
	claim["request_id"] = uuid.NewString()
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/key-packages/claim", other, od.Token, claim), 404)
	messagingCode(t, messagingRequest(t, srv, "POST", endpoint, session, device.Token, body), 409)
}

package api_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestMessagingDeliveryHTTP(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	session := messagingLogin(t, st, "alice")
	d := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
	other := messagingLogin(t, st, "outsider")
	od := verifyEnrollment(t, srv, other, requestEnrollment(t, srv, other))
	w := messagingRequest(t, srv, "POST", "/api/messaging/rooms", session, d.Token, map[string]string{"name": "Team"})
	messagingCode(t, w, 201)
	var room struct{ ID string }
	if err := json.Unmarshal(w.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	path := "/api/messaging/rooms/" + room.ID
	messagingCode(t, messagingRequest(t, srv, "GET", path+"/delivery", session, "", nil), 403)
	messagingCode(t, messagingRequest(t, srv, "GET", path+"/delivery", other, od.Token, nil), 404)
	w = messagingRequest(t, srv, "GET", path+"/delivery", session, d.Token, nil)
	messagingCode(t, w, 200)
	var state struct {
		Epoch      int64
		RosterHash string `json:"roster_hash"`
		Paused     bool
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.Epoch != 0 || !state.Paused {
		t.Fatal(state)
	}
	body := map[string]any{"id": uuid.NewString(), "kind": "commit", "epoch": 0, "roster_hash": state.RosterHash, "payload": "b3BhcXVl"}
	for _, change := range []map[string]any{
		{"epoch": nil}, {"epoch": -1}, {"epoch": 0.5}, {"kind": "plaintext"}, {"id": "bad"}, {"payload": "bad-base64"}, {"unexpected": true},
		{"payload": base64.StdEncoding.EncodeToString(make([]byte, 64*1024+1))},
		{"welcomes": map[string]string{uuid.NewString(): "not base64"}},
	} {
		bad := map[string]any{}
		for k, v := range body {
			bad[k] = v
		}
		for k, v := range change {
			bad[k] = v
		}
		messagingCode(t, messagingRequest(t, srv, "POST", path+"/events", session, d.Token, bad), 400)
	}
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/events", session, "", body), 403)
	w = messagingRequest(t, srv, "POST", path+"/events", session, d.Token, body)
	messagingCode(t, w, 200)
	first := w.Body.String()
	w = messagingRequest(t, srv, "POST", path+"/events", session, d.Token, body)
	messagingCode(t, w, 200)
	if w.Body.String() != first {
		t.Fatal("retry response differs")
	}
	body["payload"] = "b3RoZXI="
	messagingCode(t, messagingRequest(t, srv, "POST", path+"/events", session, d.Token, body), 409)
	body["id"] = uuid.NewString()
	body["kind"] = "application"
	body["epoch"] = 1
	// More than one page proves the cursor resumes without duplicates or skips.
	for i := 0; i < 52; i++ {
		body["id"] = uuid.NewString()
		messagingCode(t, messagingRequest(t, srv, "POST", path+"/events", session, d.Token, body), 200)
	}
	after := 0
	for _, count := range []int{50, 3, 0} {
		w = messagingRequest(t, srv, "GET", fmt.Sprintf("%s/events?after=%d", path, after), session, d.Token, nil)
		messagingCode(t, w, 200)
		if strings.Contains(w.Body.String(), d.Token) || strings.Contains(w.Body.String(), "request_hash") {
			t.Fatal("internal credential/digest leaked")
		}
		var page struct {
			Events []struct{ Sequence int }
			Next   int
			Start  int `json:"start_sequence"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Events) != count || page.Start != 1 {
			t.Fatal(w.Body.String())
		}
		for _, e := range page.Events {
			after++
			if e.Sequence != after {
				t.Fatal("cursor gap", e.Sequence, after)
			}
		}
		if page.Next != after {
			t.Fatal(page.Next, after)
		}
	}
	for _, cursor := range []string{"-1", "1.5", "9223372036854775808"} {
		messagingCode(t, messagingRequest(t, srv, "GET", path+"/events?after="+cursor, session, d.Token, nil), 400)
	}
	messagingCode(t, messagingRequest(t, srv, "GET", path+"/events?after=54", session, d.Token, nil), 409)
	messagingCode(t, messagingRequest(t, srv, "GET", path+"/events", other, od.Token, nil), 404)
	messagingCode(t, messagingRequest(t, srv, "PUT", path+"/events", session, d.Token, nil), 404)
}

func TestMessagingReceiveTrafficDoesNotSpendWriteBudget(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	session := messagingLogin(t, st, "busy-reader")
	d := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
	created := messagingRequest(t, srv, "POST", "/api/messaging/rooms", session, d.Token, map[string]string{"name": "Busy"})
	messagingCode(t, created, 201)
	var room struct{ ID string }
	if err := json.Unmarshal(created.Body.Bytes(), &room); err != nil {
		t.Fatal(err)
	}
	path := "/api/messaging/rooms/" + room.ID + "/delivery"
	// More than the old shared budget: ordinary receives cannot prevent an
	// invitation or device action. Writes still have an independent abuse cap.
	for range 150 {
		messagingCode(t, messagingRequest(t, srv, "GET", path, session, d.Token, nil), 200)
	}
	messagingCode(t, messagingRequest(t, srv, "POST", "/api/messaging/rooms", session, d.Token, map[string]string{"name": "Another"}), 201)
	limited := false
	for range 120 {
		w := messagingRequest(t, srv, "POST", "/api/messaging/rooms", session, d.Token, map[string]string{"name": ""})
		if w.Code == 429 {
			limited = true
			break
		}
		messagingCode(t, w, 400)
	}
	if !limited {
		t.Fatal("write abuse was not limited")
	}
	messagingCode(t, messagingRequest(t, srv, "GET", path, session, d.Token, nil), 200)
}

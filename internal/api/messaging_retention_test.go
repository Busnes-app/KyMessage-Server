package api_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestMessagingRetentionHTTP(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	session := messagingLogin(t, st, "retention")
	d := verifyEnrollment(t, srv, session, requestEnrollment(t, srv, session))
	for _, days := range []any{-1, 2, 31, 1.5, "7"} {
		messagingCode(t, messagingRequest(t, srv, "POST", "/api/messaging/rooms", session, d.Token, map[string]any{"name": "Invalid", "retention_days": days}), 400)
	}
	for _, days := range []int{0, 1, 7, 30} {
		w := messagingRequest(t, srv, "POST", "/api/messaging/rooms", session, d.Token, map[string]any{"name": "Retained", "retention_days": days})
		messagingCode(t, w, 201)
		var room struct {
			ID   string
			Days int `json:"retention_days"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &room); err != nil {
			t.Fatal(err)
		}
		expected := days
		if expected == 0 {
			expected = 30
		}
		if room.Days != expected {
			t.Fatal(room)
		}
		path := "/api/messaging/rooms/" + room.ID
		w = messagingRequest(t, srv, "GET", path+"/delivery", session, d.Token, nil)
		var state struct {
			Hash string `json:"roster_hash"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		body := map[string]any{"id": uuid.NewString(), "kind": "commit", "epoch": 0, "roster_hash": state.Hash, "payload": "b3BhcXVl"}
		messagingCode(t, messagingRequest(t, srv, "POST", path+"/events", session, d.Token, body), 200)
		w = messagingRequest(t, srv, "GET", path+"/events", session, d.Token, nil)
		messagingCode(t, w, 200)
		var page struct {
			Events []struct {
				Created int64 `json:"created_at"`
				Expires int64 `json:"expires_at"`
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Events) != 1 || page.Events[0].Expires-page.Events[0].Created != int64(expected)*86400 {
			t.Fatal(page)
		}
	}
	driver := cfg.Database.Driver
	if driver == "postgres" {
		driver = "pgx"
	}
	db, err := sql.Open(driver, cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `UPDATE messaging_events SET created_at = created_at - 400*86400`); err != nil {
		t.Fatal(err)
	}
	w := messagingRequest(t, srv, "GET", "/api/messaging/rooms", session, d.Token, nil)
	var list struct{ Rooms []struct{ ID string } }
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, room := range list.Rooms {
		w = messagingRequest(t, srv, "GET", "/api/messaging/rooms/"+room.ID+"/events", session, d.Token, nil)
		messagingCode(t, w, 410)
		if !strings.Contains(w.Body.String(), `"code":"history_expired"`) || strings.Contains(w.Body.String(), "b3BhcXVl") {
			t.Fatal(w.Body.String())
		}
	}
}

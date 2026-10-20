package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

type scimUsersReply struct {
	Enabled  bool `json:"enabled"`
	TokenSet bool `json:"token_set"`
	Users    []struct {
		Username string `json:"username"`
		Status   string `json:"status"`
	} `json:"users"`
}

func TestSCIMUsersListsOnlySCIMRowsForAdmins(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	cfg.SCIM.Enabled = true
	cfg.SCIM.BearerToken = "never-in-a-reply"
	cfg.SCIM.TokenSet = true
	for _, u := range []*store.User{
		{ID: "usr_p1", Username: "provisioned", Role: "user", Status: "active", SSOProvider: "scim"},
		{ID: "usr_p2", Username: "offboarded", Role: "user", Status: "inactive", SSOProvider: "scim"},
		{ID: "usr_k", Username: "suiteuser", Role: "user", Status: "active", SSOProvider: "kyidentity"},
	} {
		if err := st.Users().CreateUser(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}

	member := loginAs(t, srv, st, "bob", "user")
	if w := do(t, srv, "GET", "/api/admin/scim/users", member); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin: got %d, want 403", w.Code)
	}

	w := do(t, srv, "GET", "/api/admin/scim/users", loginAs(t, srv, st, "alice", "admin"))
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("admin: got %d (Cache-Control %q): %s", w.Code, w.Header().Get("Cache-Control"), w.Body.String())
	}
	if strings.Contains(w.Body.String(), "never-in-a-reply") {
		t.Fatal("the reply carries the SCIM bearer token")
	}
	var got scimUsersReply
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || !got.TokenSet {
		t.Errorf("enabled/token_set: got %v/%v, want true/true", got.Enabled, got.TokenSet)
	}
	names := map[string]string{}
	for _, u := range got.Users {
		names[u.Username] = u.Status
	}
	if len(names) != 2 || names["provisioned"] != "active" || names["offboarded"] != "inactive" {
		t.Errorf("users: got %v, want only the two scim rows", names)
	}
}

func TestSCIMUsersReportsAnUnsetToken(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	cfg.SCIM.TokenSet = false
	w := do(t, srv, "GET", "/api/admin/scim/users", loginAs(t, srv, st, "alice", "admin"))
	var got scimUsersReply
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.TokenSet || got.Users == nil || len(got.Users) != 0 {
		t.Errorf("got %s, want token_set false and an empty list", w.Body.String())
	}
}

package scim_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/scim"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

// An IdP asking whether userName X exists links to whatever comes back and then PATCHes it.
// Only an exact match on the named attribute may answer.
func TestSCIMFilterMatchesExactlyOneAttribute(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, u := range []*store.User{
		{ID: "usr_jim", Username: "jimbob@corp.com", Email: "jim@corp.com", Role: "user", Status: "active", SSOProvider: "scim"},
		{ID: "usr_mal", Username: "mallory", DisplayName: "bob@corp.com", Email: "mal@corp.com", Role: "user", Status: "active", SSOProvider: "kysignon", SSOSubject: "sub-mal"},
		{ID: "usr_bob", Username: "Bob@Corp.com", Email: "bob@corp.com", Role: "user", Status: "active", SSOProvider: "scim", SSOSubject: "ext-bob"},
	} {
		if err := st.Users().CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	token := "scim-token"
	srv := scim.NewServer(st, config.SCIMConfig{Enabled: true, BearerToken: token}, "http://localhost:8080")
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	handler := srv.AuthMiddleware(mux)

	query := func(filter string) (int, []string) {
		req := httptest.NewRequest("GET", "/scim/v2/Users?filter="+url.QueryEscape(filter), nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		var page struct {
			Resources []struct {
				ID string `json:"id"`
			} `json:"Resources"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &page)
		ids := []string{}
		for _, r := range page.Resources {
			ids = append(ids, r.ID)
		}
		return w.Code, ids
	}

	for filter, want := range map[string][]string{
		`userName eq "bob@corp.com"`:       {"usr_bob"},
		`USERNAME EQ "bob@corp.com"`:       {"usr_bob"},
		`userName eq "%"`:                  {},
		`userName eq "j_mbob@corp.com"`:    {},
		`externalId eq "sub-mal"`:          {"usr_mal"},
		`emails.value eq "BOB@corp.com"`:   {"usr_bob"},
		`displayName eq "bob@corp.com"`:    nil,
		`userName eq "x" or userName pr`:   nil,
		`title eq "a" and userName eq "b"`: nil,
	} {
		code, ids := query(filter)
		if want == nil {
			if code != http.StatusBadRequest {
				t.Errorf("%s: got %d %v, want 400", filter, code, ids)
			}
			continue
		}
		if code != http.StatusOK || len(ids) != len(want) || (len(want) == 1 && ids[0] != want[0]) {
			t.Errorf("%s: got %d %v, want %v", filter, code, ids, want)
		}
	}
}

package scim_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/scim"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"github.com/Busnes-app/ky_server_base/internal/testdb"
)

func scimDo(t *testing.T, handler http.Handler, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

// Roles are "admin" or "user"; removing a role or active takes the grant away rather than
// keeping it; groups only take SCIM's own users.
func TestSCIMRolesRemovalAndGroupMembers(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const token = "scim-secret-bearer-token"
	mux := http.NewServeMux()
	srv := scim.NewServer(st, config.SCIMConfig{Enabled: true, BearerToken: token}, "http://localhost:8080")
	srv.RegisterRoutes(mux)
	h := srv.AuthMiddleware(mux)
	for _, u := range []*store.User{
		{ID: "usr_kyid", Username: "kyid", Role: "user", Status: "active", SSOProvider: "kyidentity", SSOSubject: "sub-kyid"},
		{ID: "usr_boss", Username: "boss", Role: "admin", Status: "active", SSOProvider: "scim"},
	} {
		if err := st.Users().CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	role := func(v string) []map[string]any { return []map[string]any{{"value": v, "primary": true}} }
	user := func(name string, roles any) map[string]any {
		return map[string]any{"schemas": []string{scim.SchemaUser}, "userName": name, "roles": roles}
	}
	patchOp := func(ops ...map[string]any) map[string]any {
		return map[string]any{"schemas": []string{scim.SchemaPatchOp}, "Operations": ops}
	}

	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/scim/v2/Users", user("root", role("superadmin"))},
		{"PUT", "/scim/v2/Users/usr_boss", user("boss", role("owner"))},
		{"PATCH", "/scim/v2/Users/usr_boss", patchOp(map[string]any{"op": "replace", "path": "roles", "value": role("root")})},
	} {
		if w := scimDo(t, h, token, c.method, c.path, c.body); w.Code != http.StatusBadRequest {
			t.Errorf("%s %s with an unknown role: got %d %s", c.method, c.path, w.Code, w.Body.String())
		}
	}
	if n, _ := st.Users().CountUsers(ctx); n != 2 {
		t.Errorf("a user with an unknown role was created: %d users", n)
	}
	if u, _ := st.Users().GetUserByID(ctx, "usr_boss"); u.Role != "admin" {
		t.Errorf("refused change applied: role %q", u.Role)
	}

	if w := scimDo(t, h, token, "PATCH", "/scim/v2/Users/usr_boss", patchOp(map[string]any{"op": "remove", "path": "roles"})); w.Code != http.StatusOK {
		t.Fatalf("remove roles: %d %s", w.Code, w.Body.String())
	}
	if u, _ := st.Users().GetUserByID(ctx, "usr_boss"); u.Role != "user" || u.Status != "active" {
		t.Errorf("after removing roles: role %q status %q, want user/active", u.Role, u.Status)
	}
	if w := scimDo(t, h, token, "PATCH", "/scim/v2/Users/usr_boss", patchOp(map[string]any{"op": "remove", "path": "active"})); w.Code != http.StatusOK {
		t.Fatalf("remove active: %d %s", w.Code, w.Body.String())
	}
	if u, _ := st.Users().GetUserByID(ctx, "usr_boss"); u.Status != "inactive" {
		t.Errorf("after removing active: status %q, want inactive", u.Status)
	}

	members := func(ids ...string) map[string]any {
		m := []map[string]any{}
		for _, id := range ids {
			m = append(m, map[string]any{"value": id})
		}
		return map[string]any{"schemas": []string{"urn:ietf:params:scim:schemas:core:2.0:Group"}, "displayName": "staff", "members": m}
	}
	if w := scimDo(t, h, token, "POST", "/scim/v2/Groups", members("usr_boss", "usr_kyid")); w.Code != http.StatusBadRequest {
		t.Fatalf("group with a KyIdentity member: %d %s", w.Code, w.Body.String())
	}
	if _, total, _ := st.Groups().ListGroups(ctx, 0, 10); total != 0 {
		t.Fatalf("a refused group was created: %d", total)
	}
	w := scimDo(t, h, token, "POST", "/scim/v2/Groups", members("usr_boss"))
	if w.Code != http.StatusCreated {
		t.Fatalf("group with a SCIM member: %d %s", w.Code, w.Body.String())
	}
	var g struct{ ID string }
	_ = json.Unmarshal(w.Body.Bytes(), &g)
	if w := scimDo(t, h, token, "PUT", "/scim/v2/Groups/"+g.ID, members("usr_kyid")); w.Code != http.StatusBadRequest {
		t.Fatalf("replace members with a KyIdentity user: %d %s", w.Code, w.Body.String())
	}
	if groups, _ := st.Groups().GetUserGroups(ctx, "usr_kyid"); len(groups) != 0 {
		t.Fatalf("KyIdentity user joined a SCIM group: %+v", groups)
	}
}

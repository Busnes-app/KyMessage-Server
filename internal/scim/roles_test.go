package scim_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

// SCIM grants admin only with KY_SCIM_ALLOW_ADMIN (ky-server-base Q3 A): a create or change to
// admin is refused; an existing grant can be kept or removed, so deprovisioning still works.
func TestSCIMAdminGrantNeedsOptIn(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, testdb.Config(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	const token = "scim-secret-bearer-token"
	handler := func(allow bool) http.Handler {
		mux := http.NewServeMux()
		srv := scim.NewServer(st, config.SCIMConfig{Enabled: true, BearerToken: token, AllowAdmin: allow}, "http://localhost:8080")
		srv.RegisterRoutes(mux)
		return srv.AuthMiddleware(mux)
	}
	for _, u := range []*store.User{
		{ID: "usr_boss", Username: "boss", Role: "admin", Status: "active", SSOProvider: "scim"},
		{ID: "usr_dev", Username: "dev", Role: "user", Status: "active", SSOProvider: "scim"},
	} {
		if err := st.Users().CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	admin := []map[string]any{{"value": "admin", "primary": true}}
	user := func(name string, active bool) map[string]any {
		return map[string]any{"schemas": []string{scim.SchemaUser}, "userName": name, "roles": admin, "active": active}
	}
	h := handler(false)
	if w := scimDo(t, h, token, "POST", "/scim/v2/Users", user("root", true)); w.Code != http.StatusBadRequest {
		t.Errorf("create admin: %d %s", w.Code, w.Body.String())
	}
	if w := scimDo(t, h, token, "PATCH", "/scim/v2/Users/usr_dev", map[string]any{"schemas": []string{scim.SchemaPatchOp},
		"Operations": []map[string]any{{"op": "replace", "path": "roles", "value": admin}}}); w.Code != http.StatusBadRequest {
		t.Errorf("promote: %d %s", w.Code, w.Body.String())
	}
	if u, _ := st.Users().GetUserByID(ctx, "usr_dev"); u.Role != "user" {
		t.Errorf("refused promotion applied: %+v", u)
	}
	// Deactivating an existing admin still lands.
	if w := scimDo(t, h, token, "PUT", "/scim/v2/Users/usr_boss", user("boss", false)); w.Code != http.StatusOK {
		t.Errorf("deactivate existing admin: %d %s", w.Code, w.Body.String())
	}
	if u, _ := st.Users().GetUserByID(ctx, "usr_boss"); u.Status != "inactive" {
		t.Errorf("deactivation not applied: %+v", u)
	}
	// Turning the deactivated admin back on is a new grant (pass 2).
	if w := scimDo(t, h, token, "PATCH", "/scim/v2/Users/usr_boss", map[string]any{"schemas": []string{scim.SchemaPatchOp},
		"Operations": []map[string]any{{"op": "replace", "path": "active", "value": true}}}); w.Code != http.StatusBadRequest {
		t.Errorf("reactivate admin: %d %s", w.Code, w.Body.String())
	}
	if u, _ := st.Users().GetUserByID(ctx, "usr_boss"); u.Status != "inactive" {
		t.Errorf("refused reactivation applied: %+v", u)
	}
	if w := scimDo(t, handler(true), token, "POST", "/scim/v2/Users", user("root", true)); w.Code != http.StatusCreated {
		t.Errorf("create admin with opt-in: %d %s", w.Code, w.Body.String())
	}
}

// An empty roles collection replaces the admin grant with none (pass 2): PUT and PATCH
// replace both demote, and the demoted row's sessions go.
func TestSCIMEmptyRolesDemote(t *testing.T) {
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
		{ID: "usr_a", Username: "a", Role: "admin", Status: "active", SSOProvider: "scim"},
		{ID: "usr_b", Username: "b", Role: "admin", Status: "active", SSOProvider: "scim"},
	} {
		if err := st.Users().CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := st.Sessions().CreateSession(ctx, &store.Session{TokenHash: "sess-a", UserID: "usr_a", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, ""); err != nil {
		t.Fatal(err)
	}
	if w := scimDo(t, h, token, "PUT", "/scim/v2/Users/usr_a", map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "a", "active": true, "roles": []any{}}); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if w := scimDo(t, h, token, "PATCH", "/scim/v2/Users/usr_b", map[string]any{"schemas": []string{scim.SchemaPatchOp},
		"Operations": []map[string]any{{"op": "replace", "path": "roles", "value": []any{}}}}); w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body.String())
	}
	for _, id := range []string{"usr_a", "usr_b"} {
		if u, _ := st.Users().GetUserByID(ctx, id); u.Role != "user" {
			t.Errorf("%s kept %q", id, u.Role)
		}
	}
	if _, err := st.Sessions().GetSession(ctx, "sess-a"); err == nil {
		t.Error("the demoted admin's session survived")
	}
}

// externalId is the KyIdentity subject that adopts the row: PUT and PATCH change it, and a
// change revokes the row's sessions (pass 2: both were silently ignored).
func TestSCIMExternalIDChanges(t *testing.T) {
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
	if err := st.Users().CreateUser(ctx, &store.User{ID: "usr_sam", Username: "sam", Role: "user", Status: "active", SSOProvider: "scim"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.Sessions().CreateSession(ctx, &store.Session{TokenHash: "sess-sam", UserID: "usr_sam", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, ""); err != nil {
		t.Fatal(err)
	}
	if w := scimDo(t, h, token, "PATCH", "/scim/v2/Users/usr_sam", map[string]any{"schemas": []string{scim.SchemaPatchOp},
		"Operations": []map[string]any{{"op": "replace", "path": "externalId", "value": "kid-sam"}}}); w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body.String())
	}
	u, _ := st.Users().GetUserByID(ctx, "usr_sam")
	if u.SSOSubject != "kid-sam" {
		t.Fatalf("PATCH externalId not stored: %+v", u)
	}
	// A sign-in of the old subject read the row before the change: its session is refused.
	if err := st.Sessions().CreateSession(ctx, &store.Session{TokenHash: "sess-stale", UserID: "usr_sam", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, ""); err == nil {
		t.Fatal("a session fenced on the old subject's snapshot was issued")
	}
	if err := st.Sessions().CreateSession(ctx, &store.Session{TokenHash: "sess-new", UserID: "usr_sam", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, u.PasswordHash); err != nil {
		t.Fatalf("a fresh read cannot issue a session: %v", err)
	}
	if _, err := st.Sessions().GetSession(ctx, "sess-sam"); err == nil {
		t.Error("a session survived the identity change")
	}
	if w := scimDo(t, h, token, "PUT", "/scim/v2/Users/usr_sam", map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "sam", "externalId": "kid-sam2", "active": true}); w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if u, _ := st.Users().GetUserByID(ctx, "usr_sam"); u.SSOSubject != "kid-sam2" {
		t.Fatalf("PUT externalId not stored: %+v", u)
	}
	if w := scimDo(t, h, token, "PUT", "/scim/v2/Users/usr_sam", map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "sam", "active": true}); w.Code != http.StatusOK {
		t.Fatalf("PUT without externalId: %d %s", w.Code, w.Body.String())
	}
	if u, _ := st.Users().GetUserByID(ctx, "usr_sam"); u.SSOSubject != "kid-sam2" {
		t.Fatalf("PUT without externalId cleared it: %+v", u)
	}
}

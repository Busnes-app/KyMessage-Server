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

// SCIM owns only the rows it created: a KyIdentity row changed or deleted here would
// unlock or deactivate that person's Matrix account behind KyIdentity's back.
func TestSCIMWritesOnlyTouchSCIMRows(t *testing.T) {
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
	handler := srv.AuthMiddleware(mux)

	for _, u := range []*store.User{
		{ID: "usr_kyid", Username: "kyid", Role: "user", Status: "inactive", SSOProvider: "kyidentity", SSOSubject: "sub-kyid"},
		{ID: "usr_scim", Username: "scimuser", Role: "user", Status: "inactive", SSOProvider: "scim", SSOSubject: "ext-scim"},
	} {
		if err := st.Users().CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	do := func(method, id string, body any) int {
		var r *bytes.Reader
		if body == nil {
			r = bytes.NewReader(nil)
		} else {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		}
		req := httptest.NewRequest(method, "/scim/v2/Users/"+id, r)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}
	put := map[string]any{"schemas": []string{scim.SchemaUser}, "userName": "renamed", "active": true}
	patch := map[string]any{"schemas": []string{scim.SchemaPatchOp}, "Operations": []map[string]any{{"op": "replace", "path": "active", "value": true}}}

	for _, c := range []struct {
		method string
		body   any
	}{{"PUT", put}, {"PATCH", patch}, {"DELETE", nil}} {
		if code := do(c.method, "usr_kyid", c.body); code != http.StatusNotFound {
			t.Errorf("%s on a KyIdentity row: got %d, want 404", c.method, code)
		}
	}
	got, err := st.Users().GetUserByID(ctx, "usr_kyid")
	if err != nil {
		t.Fatalf("KyIdentity row gone: %v", err)
	}
	if got.Status != "inactive" || got.Username != "kyid" {
		t.Errorf("KyIdentity row changed: %+v", got)
	}

	if code := do("PATCH", "usr_scim", patch); code != http.StatusOK {
		t.Errorf("PATCH on a SCIM row: got %d", code)
	}
	if code := do("PUT", "usr_scim", put); code != http.StatusOK {
		t.Errorf("PUT on a SCIM row: got %d", code)
	}
	if code := do("DELETE", "usr_scim", nil); code != http.StatusNoContent {
		t.Errorf("DELETE on a SCIM row: got %d", code)
	}
}

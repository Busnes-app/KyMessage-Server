package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fmt"
	"github.com/Busnes-app/ky-primitives/applysetup"
	"github.com/Busnes-app/ky-primitives/syncauth"
	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/backup"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"strings"
)

func TestInstallerLiveActivationRolesAndSignedRevocation(t *testing.T) {
	ctx := context.Background()
	_, st, cfg := setupTestServer(t)
	cfg.Server.AppURL = "https://message.example.test"
	dir := t.TempDir()
	secret, directory := filepath.Join(dir, "client"), filepath.Join(dir, "directory")
	for _, p := range []string{secret, directory} {
		if err := os.WriteFile(p, []byte("private-fixture-secret"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	b := applysetup.Common{Schema: applysetup.Schema, Service: "kymessage", SetupID: "271333e2-c221-4601-b249-563b3d828530", SSO: &applysetup.SSO{IssuerURL: "https://identity.example.test", ClientID: "message", ClientSecretFile: secret, DirectorySecretFile: directory, RedirectURI: cfg.Server.AppURL + "/api/sso/kyidentity/callback"}, Admins: []applysetup.Identity{{Issuer: "https://identity.example.test", Subject: "owner-sub", Username: "owner-admin"}}, Everyday: []applysetup.Identity{{Issuer: "https://identity.example.test", Subject: "daily-sub", Username: "everyday"}}}
	raw, _ := json.Marshal(b)
	makeServer := func() *api.Server { return api.NewServer(cfg, st) }
	srv := makeServer()
	socket, err := config.InstallerSocket(cfg.Database.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	closeSetup, err := applysetup.StartLocal(ctx, socket, srv.ApplyInstaller, nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := applysetup.CallLocal(ctx, socket, raw, false)
	if err != nil || len(report.Created) != 3 || len(report.Pending) != 1 {
		t.Fatal(report, err)
	}
	if cfg.SSO.KyIdentityIssuer != "" {
		t.Fatal("mutable runtime snapshot")
	}
	if err = closeSetup(); err != nil {
		t.Fatal(err)
	}
	if err = config.ApplyInstallerConfiguration(cfg, b); err != nil {
		t.Fatal(err)
	}
	srv = makeServer()
	closeSetup, err = applysetup.StartLocal(ctx, socket, srv.ApplyInstaller, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSetup()
	report, err = applysetup.CallLocal(ctx, socket, raw, true)
	if err != nil || len(report.Present) != 3 || len(report.Pending) != 0 {
		t.Fatal(report, err)
	}
	for _, sub := range []string{"owner-sub", "daily-sub"} {
		user, err := st.Users().GetUserBySSO(ctx, "kyidentity", sub)
		if err != nil {
			t.Fatal(err)
		}
		login := httptest.NewRecorder()
		if _, _, err = auth.NewSessionManager(st, cfg.Security).IssueSession(ctx, login, httptest.NewRequest("GET", "/", nil), user); err != nil {
			t.Fatal(err)
		}
		request := func() *httptest.ResponseRecorder {
			r := httptest.NewRequest(http.MethodGet, "/api/backup/status", nil)
			for _, c := range login.Result().Cookies() {
				r.AddCookie(c)
			}
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			return w
		}
		expected := 200
		if sub == "daily-sub" {
			expected = 403
		}
		if w := request(); w.Code != expected {
			t.Fatal(sub, w.Code, w.Body.String())
		}
		body := fmt.Sprintf(`{"id":%q,"active":false,"meta":{"version":"W/\"2\""}}`, sub)
		syncRequest := httptest.NewRequest("POST", "/api/installer/directory", strings.NewReader(body))
		headers, err := syncauth.Sign([]byte(cfg.SSO.KyIdentityHMACSecret), time.Now(), "user.deactivated", "fixture-"+sub, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		headers.Apply(syncRequest)
		synced := httptest.NewRecorder()
		srv.ServeHTTP(synced, syncRequest)
		if synced.Code != 204 {
			t.Fatalf("signed offboard %d %s", synced.Code, synced.Body)
		}
		if w := request(); w.Code != 401 {
			t.Fatal("inactive identity retained session", sub, w.Code)
		}
	}
}

func TestInstallerNetworkRejectsUntrustedForwardedHeaders(t *testing.T) {
	_, st, cfg := setupTestServer(t)
	for _, trusted := range []bool{false, true} {
		cfg.Security.TrustedProxies = nil
		if trusted {
			cfg.Security.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("192.0.2.10/32")}
		}
		srv := api.NewServer(cfg, st)
		r := httptest.NewRequest("GET", "/api/installer/network", nil)
		r.RemoteAddr = "192.0.2.10:1234"
		r.Header.Set("X-Forwarded-For", "198.51.100.8")
		r.Header.Set("X-Forwarded-Proto", "https")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		var result struct {
			IP      string `json:"clientIp"`
			Trusted bool   `json:"proxyHeadersTrusted"`
			Scheme  string `json:"scheme"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		ip, scheme := "192.0.2.10", "http"
		if trusted {
			ip, scheme = "198.51.100.8", "https"
		}
		if result.IP != ip || result.Scheme != scheme || result.Trusted != trusted || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("network %+v", result)
		}
	}
}

func TestInstallerOIDCRejectsUnassignedAndKeepsAdminOutOfChat(t *testing.T) {
	ctx := context.Background()
	_, st, cfg := setupTestServer(t)
	cfg.Server.AppURL = "https://message.example.test"
	idp := makeFakeIdP(t, cfg, true)
	cfg.SSO.AutoProvision = false
	dir := t.TempDir()
	clientPath, directoryPath := filepath.Join(dir, "client"), filepath.Join(dir, "directory")
	for _, path := range []string{clientPath, directoryPath} {
		if err := os.WriteFile(path, []byte("disposable-private-secret"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	b := applysetup.Common{Schema: applysetup.Schema, Service: "kymessage", SetupID: "271333e2-c221-4601-b249-563b3d828530", SSO: &applysetup.SSO{IssuerURL: idp.issuer, ClientID: "client", ClientSecretFile: clientPath, DirectorySecretFile: directoryPath, RedirectURI: cfg.Server.AppURL + "/api/sso/kyidentity/callback"}, Admins: []applysetup.Identity{{Issuer: idp.issuer, Subject: "owner", Username: "owner-admin"}}, Everyday: []applysetup.Identity{{Issuer: idp.issuer, Subject: "daily", Username: "daily-user"}}}
	if _, err := st.ApplyInstaller(ctx, b, false); err != nil {
		t.Fatal(err)
	}
	if err := config.ApplyInstallerConfiguration(cfg, b); err != nil {
		t.Fatal(err)
	}
	srv := api.NewServer(cfg, st)
	statuses, err := st.Users().DirectoryStatuses(ctx, "kyidentity")
	if err != nil || statuses["owner"] != "inactive" || statuses["daily"] != "active" {
		t.Fatal(statuses, err)
	}
	actions := matrixsync.Plan([]matrixsync.User{{ID: "owner-mas", Username: "owner", Subject: "owner"}, {ID: "daily-mas", Username: "daily", Subject: "daily"}}, statuses, nil)
	if len(actions) != 1 || actions[0].Kind != matrixsync.Lock || actions[0].User.ID != "owner-mas" {
		t.Fatal("dedicated owner chat not locked", actions)
	}
	for _, sub := range []string{"owner", "daily", "unassigned"} {
		response := idp.signIn(srv, map[string]any{"sub": sub, "preferred_username": "owner-admin", "email": "owner@example.test", "role": "admin", "auth_time": time.Now().Unix()})
		if sub == "unassigned" {
			if response.Code != 403 || sessionCookie(response) != nil {
				t.Fatal("unassigned identity accepted", response.Code, response.Body)
			}
			continue
		}
		cookie := sessionCookie(response)
		if response.Code != 302 || cookie == nil {
			t.Fatal(sub, response.Code, response.Body)
		}
		want := 200
		if sub == "daily" {
			want = 403
		}
		if w := do(t, srv, "GET", "/api/admin/health", cookie); w.Code != want {
			t.Fatal(sub, w.Code, w.Body)
		}
		if sub == "owner" {
			w := do(t, srv, "GET", "/api/settings", cookie)
			var settings map[string]any
			if json.Unmarshal(w.Body.Bytes(), &settings) != nil || settings["chat_url"] != "" || strings.Contains(w.Body.String(), "installer.") {
				t.Fatal("owner ordinary chat or ownership exposed", w.Body)
			}
		}
	}
	if err := os.Remove(clientPath); err != nil {
		t.Fatal(err)
	}
	response := idp.signIn(srv, map[string]any{"sub": "owner"})
	if response.Code == 302 || sessionCookie(response) != nil {
		t.Fatal("missing credential allowed login")
	}
}

func TestInstallerManagedCredentialsSealAndDrill(t *testing.T) {
	if os.Getenv("KY_TEST_POSTGRES_DSN") != "" {
		t.Skip("native sealed snapshots require SQLite")
	}
	t.Setenv("KY_PORT", "8080")
	t.Setenv("KY_DB_DRIVER", "sqlite")
	ctx := context.Background()
	_, st, cfg := setupTestServer(t)
	cfg.Server.AppURL = "https://message.example.test"
	dir := t.TempDir()
	clientPath, directoryPath := filepath.Join(dir, "client"), filepath.Join(dir, "directory")
	for _, p := range []string{clientPath, directoryPath} {
		if err := os.WriteFile(p, []byte("disposable-private-secret"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	b := applysetup.Common{Schema: applysetup.Schema, Service: "kymessage", SetupID: "271333e2-c221-4601-b249-563b3d828530", SSO: &applysetup.SSO{IssuerURL: "https://id.example.test", ClientID: "client", ClientSecretFile: clientPath, DirectorySecretFile: directoryPath, RedirectURI: cfg.Server.AppURL + "/api/sso/kyidentity/callback"}, Admins: []applysetup.Identity{{Issuer: "https://id.example.test", Subject: "owner", Username: "owner-admin"}}}
	if _, err := st.ApplyInstaller(ctx, b, false); err != nil {
		t.Fatal(err)
	}
	if err := config.ApplyInstallerConfiguration(cfg, b); err != nil {
		t.Fatal(err)
	}
	payload, err := backup.Collect(ctx, cfg, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, f := range payload.Files {
		found[f.Path] = true
	}
	for _, name := range []string{"installer/client.secret", "installer/directory.secret", "installer/credential-paths.json"} {
		if !found[name] {
			t.Fatal("missing managed credential", name)
		}
	}
	result, err := backup.RunDrill(ctx, cfg, payload, backup.Checks)
	if err != nil || result == nil || !result.Passed {
		t.Fatal(result, err)
	}
	if err := os.Remove(directoryPath); err != nil {
		t.Fatal(err)
	}
	if _, err = backup.Collect(ctx, cfg, "fixture"); err == nil {
		t.Fatal("sealed incomplete managed backup")
	}
}

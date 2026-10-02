package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/api"
	"github.com/Busnes-app/ky_server_base/internal/auth"
	"github.com/Busnes-app/ky_server_base/internal/branding"
	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/health"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

const elementJSON = `{"default_server_config":{"m.homeserver":{"base_url":"https://matrix.example.com"}},"brand":"KyMessages","disable_guests":true}`

// withElement turns Matrix on for branding and returns Element's config path, holding body
// unless body is empty, and a running fake Element.
func withElement(t *testing.T, srv *api.Server, cfg *config.Config, body string) (string, *fakeElement) {
	t.Helper()
	cfg.Matrix.ServerName = "example.com"
	cfg.Matrix.Dir = t.TempDir()
	p := filepath.Join(cfg.Matrix.Dir, "element", "config.json")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e := &fakeElement{path: p}
	e.restart()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		e.mu.Lock()
		defer e.mu.Unlock()
		if r.URL.Path != "/config.json" || e.body == nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(e.body)
	}))
	t.Cleanup(hs.Close)
	api.SetHealthTargetsForTest(srv, health.Targets{Element: hs.URL})
	return p, e
}

// fakeElement serves /config.json as the Element image does: a copy of the file taken when
// the container starts, so only a restart picks up a change.
type fakeElement struct {
	path string
	mu   sync.Mutex
	body []byte // nil: 404
}

func (e *fakeElement) restart() {
	b, err := os.ReadFile(e.path)
	if err != nil {
		b = nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.body = b
}

// elementWith is elementJSON with brand set to name, as PatchElementBrand writes it.
func elementWith(name string) string {
	return strings.Replace(elementJSON, `"KyMessages"`, fmt.Sprintf("%q", name), 1)
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.NRGBA{R: 191, G: 63, B: 24, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// withTextChunk adds a tEXt chunk after IHDR, as a camera or an editor would.
func withTextChunk(p []byte) []byte {
	data := []byte("Comment\x00kymatrix-secret")
	c := binary.BigEndian.AppendUint32(nil, uint32(len(data)))
	c = append(c, "tEXt"...)
	c = append(c, data...)
	c = binary.BigEndian.AppendUint32(c, crc32.ChecksumIEEE(append([]byte("tEXt"), data...)))
	return append(append(append([]byte{}, p[:33]...), c...), p[33:]...)
}

// rawDo sends body as is with contentType, as the browser's logo upload does.
func rawDo(t *testing.T, srv *api.Server, session *http.Cookie, method, path, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(session)
	req.AddCookie(&http.Cookie{Name: auth.CSRFCookieName, Value: "test-csrf"})
	req.Header.Set(auth.HeaderCSRF, "test-csrf")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

type brandingBody struct {
	Name        string `json:"name"`
	StoredName  string `json:"stored_name"`
	DefaultName string `json:"default_name"`
	Logo        struct {
		Custom bool   `json:"custom"`
		SHA256 string `json:"sha256"`
		Size   int    `json:"size"`
	} `json:"logo"`
	Element *struct {
		Brand       *string `json:"brand"`
		Error       string  `json:"error"`
		Served      *string `json:"served"`
		ServedError string  `json:"served_error"`
	} `json:"element"`
}

func TestBrandNameRenamesTheConsoleAndElement(t *testing.T) {
	ctx := context.Background()
	srv, st, cfg := setupTestServer(t)
	p, _ := withElement(t, srv, cfg, elementJSON)
	before, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	admin := loginAs(t, srv, st, "root", "admin")
	path := "/api/admin/branding/name"
	got := decode[brandingBody](t, adminDo(t, srv, admin, "PUT", path, map[string]string{"name": "  Acme Chat  "}))
	if got.Name != "Acme Chat" || got.StoredName != "Acme Chat" || got.DefaultName != cfg.Server.AppName ||
		got.Element == nil || got.Element.Brand == nil || *got.Element.Brand != "Acme Chat" || got.Element.Error != "" {
		t.Fatalf("%+v", got)
	}
	if b, _ := os.ReadFile(p); string(b) != elementWith("Acme Chat") {
		t.Fatalf("Element config: %s", b)
	}
	if after, _ := os.Stat(p); !os.SameFile(before, after) {
		t.Fatal("Element's config was replaced, not rewritten in place")
	}
	if s := decode[map[string]any](t, do(t, srv, "GET", "/api/settings", nil)); s["app_name"] != "Acme Chat" {
		t.Fatalf("login page name: %v", s["app_name"])
	}
	rows := auditRows(t, st, "admin.brand_name")
	if want := fmt.Sprintf(`outcome="saved" old=%q new="Acme Chat"`, cfg.Server.AppName); len(rows) != 1 || rows[0].Details != want || rows[0].UserID != "usr_root" {
		t.Fatalf("audit %+v", rows)
	}
	// Blank resets to KY_APP_NAME.
	got = decode[brandingBody](t, adminDo(t, srv, admin, "PUT", path, map[string]string{"name": " "}))
	if got.Name != cfg.Server.AppName || got.StoredName != "" {
		t.Fatalf("after reset: %+v", got)
	}
	if b, _ := os.ReadFile(p); string(b) != elementWith(cfg.Server.AppName) {
		t.Fatalf("Element config after reset: %s", b)
	}
	if _, err := st.Settings().GetSetting(ctx, "brand_name"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("reset left brand_name: %v", err)
	}
	if w := adminDo(t, srv, admin, "GET", "/api/admin/branding", nil); w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET branding: %d %q", w.Code, w.Header().Get("Cache-Control"))
	}
}

func TestBrandNameRefusals(t *testing.T) {
	ctx := context.Background()
	srv, st, cfg := setupTestServer(t)
	withElement(t, srv, cfg, elementJSON)
	path := "/api/admin/branding/name"
	if w := adminDo(t, srv, staleAdmin(t, st), "PUT", path, map[string]string{"name": "Acme"}); w.Code != http.StatusForbidden ||
		!strings.Contains(w.Body.String(), `"code":"reauthentication_required"`) {
		t.Fatalf("stale admin: %d %s", w.Code, w.Body.String())
	}
	admin := loginAs(t, srv, st, "root", "admin")
	for _, bad := range []string{strings.Repeat("x", 65), "Acme\u202eChat", "Ac\nme"} {
		if w := adminDo(t, srv, admin, "PUT", path, map[string]string{"name": bad}); w.Code != http.StatusBadRequest {
			t.Errorf("%q: %d", bad, w.Code)
		}
	}
	for _, body := range []any{map[string]any{"name": 5}, map[string]any{"name": "A", "extra": 1}, map[string]any{}} {
		if w := adminDo(t, srv, admin, "PUT", path, body); w.Code != http.StatusBadRequest {
			t.Errorf("%v: %d", body, w.Code)
		}
	}
	if _, err := st.Settings().GetSetting(ctx, "brand_name"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused name was saved: %v", err)
	}
	rows := auditRows(t, st, "admin.brand_name")
	if len(rows) != 3 {
		t.Fatalf("want the 3 invalid names audited, not the stale session or the malformed bodies: %+v", rows)
	}
	for _, r := range rows {
		if !strings.HasPrefix(r.Details, `outcome="refused: the name `) {
			t.Errorf("details %q", r.Details)
		}
	}
}

// Element's file is missing: the name is still saved, and the answer says what Element shows.
func TestBrandNameSavedWhenElementCannotBeWritten(t *testing.T) {
	ctx := context.Background()
	srv, st, cfg := setupTestServer(t)
	p, _ := withElement(t, srv, cfg, "")
	got := decode[brandingBody](t, adminDo(t, srv, loginAs(t, srv, st, "root", "admin"), "PUT", "/api/admin/branding/name", map[string]string{"name": "Acme"}))
	if got.Name != "Acme" || got.Element == nil || got.Element.Brand != nil || got.Element.Error == "" {
		t.Fatalf("%+v", got)
	}
	if v, err := st.Settings().GetSetting(ctx, "brand_name"); err != nil || v != "Acme" {
		t.Fatalf("name not saved: %q %v", v, err)
	}
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the app created Element's config")
	}
	rows := auditRows(t, st, "admin.brand_name")
	if len(rows) != 1 || !strings.HasPrefix(rows[0].Details, `outcome="saved; Element not updated: `) {
		t.Fatalf("audit %+v", rows)
	}
}

func TestLogoUploadNormalisesServesAndResets(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	saved := decode[brandingBody](t, rawDo(t, srv, admin, "PUT", "/api/admin/branding/logo", "image/png", withTextChunk(testPNG(t, 32, 32))))
	if !saved.Logo.Custom || len(saved.Logo.SHA256) != 64 || saved.Logo.Size == 0 {
		t.Fatalf("%+v", saved)
	}
	icon := do(t, srv, "GET", "/app-icon.png", nil) // public: the login page shows it
	body := icon.Body.Bytes()
	sum := sha256.Sum256(body)
	h := icon.Header()
	if icon.Code != http.StatusOK || h.Get("Content-Type") != "image/png" || h.Get("Cache-Control") != "no-cache" ||
		h.Get("X-Content-Type-Options") != "nosniff" || h.Get("ETag") != `"`+saved.Logo.SHA256+`"` ||
		hex.EncodeToString(sum[:]) != saved.Logo.SHA256 || len(body) != saved.Logo.Size {
		t.Fatalf("served logo: %d %v", icon.Code, h)
	}
	if bytes.Contains(body, []byte("tEXt")) || bytes.Contains(body, []byte("kymatrix-secret")) {
		t.Fatal("the text chunk was served")
	}
	if _, err := png.Decode(bytes.NewReader(body)); err != nil {
		t.Fatalf("served logo does not decode: %v", err)
	}
	revalidate := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/app-icon.png", nil)
		req.Header.Set("If-None-Match", `"`+saved.Logo.SHA256+`"`)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w
	}
	if w := revalidate(); w.Code != http.StatusNotModified {
		t.Fatalf("revalidation with the current ETag: %d", w.Code)
	}
	if strings.Contains(do(t, srv, "GET", "/api/settings", admin).Body.String(), "brand_logo") {
		t.Fatal("the logo rides on /api/settings")
	}
	// Reset: a browser revalidating its custom copy must get the stamp back.
	if got := decode[brandingBody](t, adminDo(t, srv, admin, "DELETE", "/api/admin/branding/logo", nil)); got.Logo.Custom {
		t.Fatalf("after reset: %+v", got)
	}
	embedded, err := os.ReadFile("../../web/dist/app-icon.png")
	if err != nil {
		t.Fatal(err)
	}
	if w := revalidate(); w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), embedded) || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("after reset: %d, %d bytes, Cache-Control %q", w.Code, w.Body.Len(), w.Header().Get("Cache-Control"))
	}
	rows := auditRows(t, st, "admin.brand_logo")
	if len(rows) != 2 || rows[0].Details != `outcome="reset"` ||
		rows[1].Details != fmt.Sprintf(`outcome="saved" sha256=%q size="%d"`, saved.Logo.SHA256, saved.Logo.Size) {
		t.Fatalf("audit %+v", rows)
	}
}

func TestLogoUploadRefusals(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := setupTestServer(t)
	path := "/api/admin/branding/logo"
	if w := rawDo(t, srv, staleAdmin(t, st), "PUT", path, "image/png", testPNG(t, 4, 4)); w.Code != http.StatusForbidden {
		t.Fatalf("stale admin: %d", w.Code)
	}
	admin := loginAs(t, srv, st, "root", "admin")
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil); err != nil {
		t.Fatal(err)
	}
	// n bytes that start like a PNG and are not one.
	signed := func(n int) []byte {
		b := make([]byte, n)
		copy(b, "\x89PNG\r\n\x1a\n")
		return b
	}
	for name, tc := range map[string]struct {
		contentType string
		body        []byte
		code        int
		outcome     string
	}{
		"SVG":            {"image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), 415, "refused: the logo must be a PNG image (image/png)"},
		"no type":        {"", testPNG(t, 4, 4), 415, "refused: the logo must be a PNG image (image/png)"},
		"JPEG as PNG":    {"image/png", jpg.Bytes(), 400, "refused: " + branding.ErrNotPNG.Error()},
		"1025 px":        {"image/png", testPNG(t, 1025, 1), 400, "refused: " + branding.ErrLogoDimensions.Error()},
		"exactly 1 MiB":  {"image/png", signed(1 << 20), 400, "refused: " + branding.ErrNotPNG.Error()},
		"1 MiB + 1 byte": {"image/png", signed(1<<20 + 1), 413, "refused: " + branding.ErrLogoTooLarge.Error()},
	} {
		if w := rawDo(t, srv, admin, "PUT", path, tc.contentType, tc.body); w.Code != tc.code {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if rows := auditRows(t, st, "admin.brand_logo"); len(rows) == 0 || rows[0].Details != fmt.Sprintf("outcome=%q", tc.outcome) {
			t.Errorf("%s: newest audit row %+v", name, rows)
		}
	}
	if _, err := st.Settings().GetSetting(ctx, "brand_logo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused logo was stored: %v", err)
	}
}

// Review Focus 3: the minute tick neither rewrites a matching file nor logs every minute.
func TestReconcileWritesOnlyOnChangeAndLogsOncePerStreak(t *testing.T) {
	ctx := context.Background()
	srv, st, cfg := setupTestServer(t)
	p, _ := withElement(t, srv, cfg, elementWith(cfg.Server.AppName))
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	old := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := srv.ReconcileBrand(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if fi, _ := os.Stat(p); !fi.ModTime().Equal(old) {
		t.Fatal("an unchanged brand was rewritten")
	}
	if err := st.Settings().SetSetting(ctx, "brand_name", "Acme"); err != nil {
		t.Fatal(err)
	}
	if err := srv.ReconcileBrand(ctx); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != elementWith("Acme") {
		t.Fatalf("not reconciled: %s", b)
	}
	if err := os.WriteFile(p, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := srv.ReconcileBrand(ctx); err == nil {
			t.Fatal("a broken config reconciled")
		}
	}
	if err := os.WriteFile(p, []byte(elementJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := srv.ReconcileBrand(ctx); err != nil {
		t.Fatal(err)
	}
	for line, want := range map[string]int{"Element brand not updated": 1, "Element brand updated again": 1} {
		if n := strings.Count(logs.String(), line); n != want {
			t.Errorf("%q logged %d times, want %d:\n%s", line, n, want, logs.String())
		}
	}
	// Without Matrix nothing is read or written.
	cfg.Matrix.ServerName = ""
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := srv.ReconcileBrand(ctx); err != nil {
		t.Fatalf("without Matrix: %v", err)
	}
}

// Review Focus 4: a save and a tick are two writers of one file; they must not interleave.
// Run it with -race: without brandMu the writes rarely interleave visibly, but the streak
// fields race every time.
func TestConcurrentSavesAndTicksLeaveValidConfig(t *testing.T) {
	ctx := context.Background()
	srv, st, cfg := setupTestServer(t)
	p, _ := withElement(t, srv, cfg, elementJSON)
	admin := loginAs(t, srv, st, "root", "admin")
	names := []string{"A", strings.Repeat("Long brand ", 5) + "end", "Mid size"}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			adminDo(t, srv, admin, "PUT", "/api/admin/branding/name", map[string]string{"name": names[i%len(names)]})
		}()
		go func() {
			defer wg.Done()
			_ = srv.ReconcileBrand(ctx)
		}()
	}
	wg.Wait()
	b, _ := os.ReadFile(p)
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("two writers interleaved: %v\n%s", err, b)
	}
	stored, err := st.Settings().GetSetting(ctx, "brand_name")
	if err != nil || got["brand"] != stored || got["disable_guests"] != true {
		t.Fatalf("brand %v, stored %q (%v)", got["brand"], stored, err)
	}
}

// Review Focus 5: KY_APP_NAME is the service name capsules are sealed under and KyRecovery
// pins; the console name is display only.
func TestBrandNameNeverRenamesTheService(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	admin := loginAs(t, srv, st, "root", "admin")
	decode[brandingBody](t, adminDo(t, srv, admin, "PUT", "/api/admin/branding/name", map[string]string{"name": "Acme"}))
	if got := statusOf(t, srv, admin)["app_name"]; got != cfg.Server.AppName {
		t.Fatalf("backup status names the service %v, want %q", got, cfg.Server.AppName)
	}
}

// Review Focus 1: the logo would ride on every admin page load.
func TestSettingsCarryTheEffectiveNameButNotTheLogo(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := setupTestServer(t)
	for k, v := range map[string]string{"brand_name": "Acme", "brand_logo": strings.Repeat("A", 1<<20)} {
		if err := st.Settings().SetSetting(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	w := do(t, srv, "GET", "/api/settings", loginAs(t, srv, st, "root", "admin"))
	var out struct {
		AppName string            `json:"app_name"`
		Extra   map[string]string `json:"extra_settings"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if _, found := out.Extra["brand_logo"]; out.AppName != "Acme" || out.Extra == nil || found || w.Body.Len() > 64<<10 {
		t.Fatalf("app_name %q, logo in extra_settings %v, %d bytes", out.AppName, found, w.Body.Len())
	}
}

func TestBrandingWithoutMatrixHasNoElement(t *testing.T) {
	srv, st, _ := setupTestServer(t)
	w := adminDo(t, srv, loginAs(t, srv, st, "root", "admin"), "GET", "/api/admin/branding", nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `"element"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

// A logo reset needs a recent sign-in like the uploads, and a refused one changes nothing.
func TestLogoResetNeedsFreshAdmin(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := setupTestServer(t)
	if err := st.Settings().SetSetting(ctx, "brand_logo", "c3RheQ=="); err != nil {
		t.Fatal(err)
	}
	w := adminDo(t, srv, staleAdmin(t, st), "DELETE", "/api/admin/branding/logo", nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"reauthentication_required"`) {
		t.Fatalf("stale admin: %d %s", w.Code, w.Body.String())
	}
	if v, err := st.Settings().GetSetting(ctx, "brand_logo"); err != nil || v != "c3RheQ==" {
		t.Fatalf("a refused reset changed the logo: %q %v", v, err)
	}
	if rows := auditRows(t, st, "admin.brand_logo"); len(rows) != 0 {
		t.Fatalf("a refused reset was audited: %+v", rows)
	}
}

func TestAuditKindBranding(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := setupTestServer(t)
	for _, action := range []string{"admin.brand_name", "admin.brand_logo", "admin.backup_run", "matrix.lock"} {
		if err := st.Audit().LogAudit(ctx, &store.AuditRecord{Action: action, Details: `outcome="saved"`}); err != nil {
			t.Fatal(err)
		}
	}
	type auditPage struct {
		Records []struct{ Action string }
		Total   int
	}
	page := decode[auditPage](t, adminDo(t, srv, loginAs(t, srv, st, "root", "admin"), "GET", "/api/admin/audit?kind=branding", nil))
	if page.Total != 2 || len(page.Records) != 2 || page.Records[0].Action != "admin.brand_logo" || page.Records[1].Action != "admin.brand_name" {
		t.Fatalf("branding kind: %+v", page)
	}
}

// Element serves a copy of config.json made when it starts: after a rename the view reports
// the file and what Element serves apart until Element restarts.
func TestBrandingReportsWhatElementServes(t *testing.T) {
	srv, st, cfg := setupTestServer(t)
	p, element := withElement(t, srv, cfg, elementJSON)
	admin := loginAs(t, srv, st, "root", "admin")
	got := decode[brandingBody](t, adminDo(t, srv, admin, "PUT", "/api/admin/branding/name", map[string]string{"name": "Acme"}))
	if e := got.Element; e == nil || e.Brand == nil || *e.Brand != "Acme" || e.Served == nil || *e.Served != "KyMessages" || e.ServedError != "" {
		t.Fatalf("before the restart: %+v", got.Element)
	}
	element.restart()
	got = decode[brandingBody](t, adminDo(t, srv, admin, "GET", "/api/admin/branding", nil))
	if e := got.Element; e.Served == nil || *e.Served != "Acme" || e.ServedError != "" {
		t.Fatalf("after the restart: %+v", e)
	}
	// Unknown is reported as such, never as a brand: Element answers 404, a redirect (not
	// followed) or a body that is not a config.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	element.restart()
	for _, h := range []http.Handler{
		nil, // the fake Element, now 404
		http.RedirectHandler("http://example.com/config.json", http.StatusFound),
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("{")) }),
	} {
		if h != nil {
			hs := httptest.NewServer(h)
			t.Cleanup(hs.Close)
			api.SetHealthTargetsForTest(srv, health.Targets{Element: hs.URL})
		}
		got = decode[brandingBody](t, adminDo(t, srv, admin, "GET", "/api/admin/branding", nil))
		if e := got.Element; e.Served != nil || e.ServedError == "" {
			t.Errorf("unknown served brand reported as %+v", e)
		}
	}
}

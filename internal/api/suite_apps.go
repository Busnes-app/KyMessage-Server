package api

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/Busnes-app/ky-primitives/suitedir"
	"github.com/Busnes-app/ky_server_base/web"
)

// suiteAppsTTL bounds KyIdentity load: one directory call per user (and one for Element's home
// page) a minute. Failures are cached too, so a down KyIdentity is not hammered.
const suiteAppsTTL = time.Minute

// ElementHomePath is Element's embedded_pages.home_url path on the console.
const ElementHomePath = "/element-home.html"

const elementMarkPrefix = "/element-home/marks/"

type suiteAppsEntry struct {
	apps  []suitedir.App
	until time.Time
}

// suiteApps lists the suite apps sub may open, "" every launchable app, from a per-sub cache.
// Without suite sign-in, or on any error, the list is empty; the error is logged, never the
// sub or the client secret.
func (s *Server) suiteApps(ctx context.Context, sub string) []suitedir.App {
	now := time.Now()
	s.suiteMu.Lock()
	e, ok := s.suiteCache[sub]
	s.suiteMu.Unlock()
	if ok && now.Before(e.until) {
		return e.apps
	}

	apps := []suitedir.App{}
	c, err := s.kyidentity.SuiteDirectory()
	if err == nil && c != nil {
		// A dropped request must not cache an empty list for everyone after it.
		var got []suitedir.App
		if got, err = c.Apps(context.WithoutCancel(ctx), sub); err == nil {
			apps = got
		}
	}
	if err != nil {
		log.Printf("[SUITE] app list unavailable: %v", err)
	}

	s.suiteMu.Lock()
	defer s.suiteMu.Unlock()
	if s.suiteCache == nil {
		s.suiteCache = map[string]suiteAppsEntry{}
	}
	for k, old := range s.suiteCache {
		if !now.Before(old.until) {
			delete(s.suiteCache, k)
		}
	}
	s.suiteCache[sub] = suiteAppsEntry{apps, now.Add(suiteAppsTTL)}
	return apps
}

// handleSuiteApps is the console's app rail: the apps the signed-in KyIdentity user may open.
func (s *Server) handleSuiteApps(w http.ResponseWriter, r *http.Request) {
	user, _, err := s.sessions.AuthenticateRequest(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	apps := []suitedir.App{}
	if user.SSOProvider == "kyidentity" && user.SSOSubject != "" {
		apps = s.suiteApps(r.Context(), user.SSOSubject)
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

// Element fetches this into its signed-in Home view and sanitises it: no class, no <style>, no
// data: images, style attributes only on span and img. So the layout is inline styles on spans
// and the marks are https URLs on this console.
var elementHome = template.Must(template.New("home").Parse(`<h1>Welcome to {{.Name}}</h1>
{{- if .Apps}}
<p>Other apps</p>
<p>{{range .Apps}}<a href="{{.URL}}"><span style="display:inline-flex;flex-direction:column;align-items:center;gap:8px;width:104px;margin:0 4px 12px;text-align:center"><img src="{{.Mark}}" alt="" style="width:48px;height:48px;border-radius:10px"><span>{{.Name}}</span></span></a>{{end}}</p>
{{- end}}
`))

type elementHomeApp struct{ URL, Mark, Name string }

// handleElementHome serves Element's home page. Element has no console session here, so it
// lists every launchable suite app (KyIdentity still decides at each app's sign-in) but
// KyMessages itself. Only the chat host may read it cross-origin, without credentials.
func (s *Server) handleElementHome(w http.ResponseWriter, r *http.Request) {
	name, err := s.effectiveName(r.Context())
	if err != nil {
		name = s.config.Server.AppName
	}
	var apps []elementHomeApp
	for _, a := range s.suiteApps(r.Context(), "") {
		if a.ID == "kymessage" || a.ID == "kymessage-chat" {
			continue
		}
		apps = append(apps, elementHomeApp{a.URL, s.config.Server.AppURL + elementMarkPrefix + url.PathEscape(a.ID), a.Name})
	}
	h := w.Header()
	h.Del("Access-Control-Allow-Credentials")
	h.Del("Access-Control-Allow-Origin")
	if chat := s.config.Matrix.ChatHost; chat != "" {
		h.Set("Access-Control-Allow-Origin", chat)
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	if err := elementHome.Execute(w, struct {
		Name string
		Apps []elementHomeApp
	}{name, apps}); err != nil {
		log.Printf("[SUITE] Element home page: %v", err)
	}
}

var markID = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)

// handleElementMark serves a ky-ui app mark for Element's home page.
func (s *Server) handleElementMark(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !markID.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	m, err := web.AppMark(id)
	if err != nil {
		log.Printf("[SUITE] app marks: %v", err)
		http.Error(w, "mark unavailable", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", m.ContentType)
	h.Set("Content-Security-Policy", "default-src 'none'")
	h.Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(m.Body)
}

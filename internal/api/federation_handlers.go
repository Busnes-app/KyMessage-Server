package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/matrixinit"
	"github.com/Busnes-app/ky_server_base/internal/matrixsync"
	"github.com/Busnes-app/ky_server_base/internal/store"
	"gopkg.in/yaml.v3"
)

// Settings behind Settings > Federation. Servers and presets are JSON arrays of server names.
const (
	fedLevelKey   = "federation_level"
	fedServersKey = "federation_servers"
	fedPresetsKey = "federation_presets"
	// Servers taken off the file that the running Synapse may still allow: it reads the file
	// only at start.
	fedUnappliedKey = "federation_unapplied"
)

// Federation levels, narrowest first. Approval always means the operator adds the server.
const (
	fedClosed  = "closed"
	fedManual  = "manual"
	fedCurated = "curated"
)

var fedLevels = []string{fedClosed, fedManual, fedCurated}

const fedTimeout = 10 * time.Second

type fedState struct {
	Level   string
	Servers []string
	Presets []string
}

// allow is the effective allow-list: manual servers, plus ticked presets at the curated level
// that are still in this release's list. Closed is empty.
func (st fedState) allow() []string {
	if st.Level == fedClosed {
		return nil
	}
	out := slices.Clone(st.Servers)
	if st.Level == fedCurated {
		for _, p := range st.Presets {
			if presetByName(p) != nil {
				out = append(out, p)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (s *Server) fedState(ctx context.Context) (fedState, error) {
	st := fedState{Level: fedClosed}
	get := func(key string) (string, error) {
		v, err := s.store.Settings().GetSetting(ctx, key)
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		return v, err
	}
	level, err := get(fedLevelKey)
	if err != nil {
		return st, err
	}
	if level != "" {
		if !slices.Contains(fedLevels, level) {
			return st, errors.New("stored federation level is not one of closed, manual, curated")
		}
		st.Level = level
	}
	for key, dst := range map[string]*[]string{fedServersKey: &st.Servers, fedPresetsKey: &st.Presets} {
		v, err := get(key)
		if err != nil {
			return st, err
		}
		if v != "" {
			if err := json.Unmarshal([]byte(v), dst); err != nil {
				return st, errors.New(key + " is not a JSON list")
			}
		}
	}
	return st, nil
}

func (s *Server) saveFedList(ctx context.Context, key string, names []string) error {
	b, _ := json.Marshal(names)
	return s.store.Settings().SetSetting(ctx, key, string(b))
}

func (s *Server) federationFile() string {
	return filepath.Join(s.config.Matrix.Dir, matrixinit.FederationFile)
}

// ReconcileFederation writes Synapse's federation file from the settings. fedMu makes it the
// file's only writer in this process. It writes only on change and never on a settings read
// error, so a broken store cannot open or close federation. Synapse applies it at its next
// start, so it then checks which removed servers Synapse still allows. No-op without Matrix.
func (s *Server) ReconcileFederation(ctx context.Context) error {
	if !s.config.Matrix.Enabled() {
		return nil
	}
	s.fedMu.Lock()
	defer s.fedMu.Unlock()
	return errors.Join(s.reconcileFederationLocked(ctx), s.confirmUnappliedLocked(ctx))
}

// fileAllowList is the allow-list in a federation file; nil when it cannot be read.
func fileAllowList(b []byte) []string {
	var f struct {
		Allow []string `yaml:"federation_domain_whitelist"`
	}
	if yaml.Unmarshal(b, &f) != nil {
		return nil
	}
	return f.Allow
}

func (s *Server) unapplied(ctx context.Context) ([]string, error) {
	var names []string
	v, err := s.store.Settings().GetSetting(ctx, fedUnappliedKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err == nil && v != "" {
		err = json.Unmarshal([]byte(v), &names)
	}
	return names, err
}

// markUnapplied records the servers a rewrite from old to allow takes away, before the write:
// the running Synapse keeps allowing them until it restarts. Servers allowed again leave the list.
func (s *Server) markUnapplied(ctx context.Context, old, allow []string) error {
	names, err := s.unapplied(ctx)
	if err != nil {
		return err
	}
	out := slices.DeleteFunc(append(slices.Clone(names), old...), func(n string) bool { return slices.Contains(allow, n) })
	slices.Sort(out)
	if out = slices.Compact(out); slices.Equal(out, names) {
		return nil
	}
	return s.saveFedList(ctx, fedUnappliedKey, out)
}

// confirmUnappliedLocked drops each unapplied server the running Synapse now refuses.
func (s *Server) confirmUnappliedLocked(ctx context.Context) error {
	names, err := s.unapplied(ctx)
	if err != nil || len(names) == 0 {
		return err
	}
	refused := make([]bool, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		wg.Go(func() { refused[i] = s.synapseRefuses(ctx, n) })
	}
	wg.Wait()
	keep := []string{}
	for i, n := range names {
		if !refused[i] {
			keep = append(keep, n)
		}
	}
	if len(keep) == len(names) {
		return nil
	}
	return s.saveFedList(ctx, fedUnappliedKey, keep)
}

// synapseRefuses reports whether the running Synapse refuses federation from name. It checks
// its allow-list before any signature (synapse/federation/transport/server/_base.py), so a
// request claiming to come from name is answered at once: 403 M_FORBIDDEN when name is not
// allowed, 404 M_UNRECOGNIZED when Synapse does not federate. Anything else (a signature check,
// a failure) means it may still allow name.
func (s *Server) synapseRefuses(ctx context.Context, name string) bool {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	q := url.Values{"user_id": {"@kymessages-federation-check:" + s.config.Matrix.ServerName}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.matrixTargets.Synapse+"/_matrix/federation/v1/query/profile?"+q.Encode(), nil)
	if err != nil {
		return false
	}
	// name passed ValidServerName when it was saved: no quote can end the parameter.
	req.Header.Set("Authorization", `X-Matrix origin="`+name+`",destination="`+s.config.Matrix.ServerName+`",key="ed25519:kymessages",sig="a3ltZXNzYWdlcw"`)
	resp, err := s.probeHTTP.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var e struct {
		Errcode string `json:"errcode"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
	return (resp.StatusCode == http.StatusForbidden && e.Errcode == "M_FORBIDDEN") ||
		(resp.StatusCode == http.StatusNotFound && e.Errcode == "M_UNRECOGNIZED")
}

func (s *Server) reconcileFederationLocked(ctx context.Context) error {
	st, err := s.fedState(ctx)
	var want []byte
	if err == nil {
		want, err = matrixinit.FederationYAML(s.config.Matrix.ServerName, st.allow())
	}
	if err == nil {
		var have []byte
		if have, err = os.ReadFile(s.federationFile()); err == nil && !bytes.Equal(have, want) {
			if err = s.markUnapplied(ctx, fileAllowList(have), st.allow()); err == nil {
				err = matrixinit.WriteFederation(s.federationFile(), s.config.Matrix.ServerName, st.allow())
			}
			if err == nil {
				log.Printf("[FEDERATION] Synapse federation file written: level %s, %d servers; restart Synapse to apply", st.Level, len(st.allow()))
			}
		}
	}
	switch {
	case err != nil && !s.fedFailing:
		log.Printf("[FEDERATION] federation file not updated (retrying every minute): %v", err)
	case err == nil && s.fedFailing:
		log.Printf("[FEDERATION] federation file updated again")
	}
	s.fedFailing, s.fedErr = err != nil, ""
	if err != nil {
		s.fedErr = err.Error()
	}
	return err
}

// federationAllows reports whether name is on the effective allow-list now.
func (s *Server) federationAllows(ctx context.Context, name string) (bool, error) {
	st, err := s.fedState(ctx)
	if err != nil {
		return false, err
	}
	return slices.Contains(st.allow(), name), nil
}

type fedServerView struct {
	Name string `json:"name"`
}

type fedPresetView struct {
	federationPreset
	Ticked bool `json:"ticked"`
}

type federationView struct {
	Level       string          `json:"level"`
	ServerName  string          `json:"server_name"`
	Servers     []fedServerView `json:"servers"`
	Presets     []fedPresetView `json:"presets"`
	RestartHint string          `json:"restart_hint"`
	FileError   string          `json:"file_error,omitempty"`
	// Removed servers the running Synapse may still allow until it restarts.
	Unapplied []string `json:"unapplied"`
}

func (s *Server) writeFederation(ctx context.Context, w http.ResponseWriter) {
	st, err := s.fedState(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not read federation settings")
		return
	}
	v := federationView{Level: st.Level, ServerName: s.config.Matrix.ServerName,
		Servers: []fedServerView{}, Presets: []fedPresetView{}, RestartHint: s.config.Matrix.SynapseRestartHint}
	for _, n := range st.Servers {
		v.Servers = append(v.Servers, fedServerView{Name: n})
	}
	for _, p := range federationPresets {
		v.Presets = append(v.Presets, fedPresetView{federationPreset: p, Ticked: slices.Contains(st.Presets, p.Name)})
	}
	s.fedMu.Lock()
	v.FileError = s.fedErr
	v.Unapplied, err = s.unapplied(ctx)
	s.fedMu.Unlock()
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not read federation settings")
		return
	}
	if v.Unapplied == nil {
		v.Unapplied = []string{}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleFederation(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), fedTimeout)
	defer cancel()
	s.fedMu.Lock()
	err := s.confirmUnappliedLocked(ctx) // a restarted Synapse clears the warning on reload
	s.fedMu.Unlock()
	if err != nil {
		log.Printf("[FEDERATION] could not record what Synapse applies: %v", err)
	}
	s.writeFederation(ctx, w)
}

// fedPlan is what a federation request would do: save runs only after the step-up check.
// A non-zero status refuses the request with outcome as its reason.
type fedPlan struct {
	widens  bool
	outcome string
	status  int
	save    func(ctx context.Context) error
}

// fedChange plans and applies one settings change under fedMu on a detached context, writes
// the file and audits. A plan that widens (lets more servers in) needs a sign-in from the last
// stepUpWindow, like every requireFreshAdmin route; narrowing never waits on step-up, so
// closing federation is always one click.
func (s *Server) fedChange(w http.ResponseWriter, r *http.Request, action, resource string, plan func(st fedState) fedPlan) {
	if !s.matrixOn(w) {
		return
	}
	user, sess, err := s.sessions.AuthenticateRequest(r)
	if err != nil {
		s.writeError(w, http.StatusUnauthorized, "Authentication required")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), fedTimeout)
	defer cancel()
	s.fedMu.Lock()
	st, err := s.fedState(ctx)
	if err != nil {
		s.fedMu.Unlock()
		s.writeError(w, http.StatusInternalServerError, "Could not read federation settings")
		return
	}
	p := plan(st)
	if p.widens && time.Since(sess.CreatedAt) > stepUpWindow {
		s.fedMu.Unlock()
		s.writeStepUp(w, user)
		return
	}
	outcome, status := p.outcome, p.status
	if status == 0 && p.save != nil {
		err = p.save(ctx)
	}
	if err == nil && status == 0 {
		if ferr := s.reconcileFederationLocked(ctx); ferr != nil {
			outcome += "; file not updated: " + ferr.Error()
		}
	}
	s.fedMu.Unlock()
	if err != nil {
		outcome = "error: " + err.Error()
		status = http.StatusInternalServerError
	}
	s.audit(ctx, user.ID, r, action, resource, auditFields(outcome))
	if status != 0 {
		msg := strings.TrimPrefix(outcome, "refused: ")
		if err != nil {
			msg = "Could not save the federation change"
		}
		s.writeError(w, status, msg)
		return
	}
	s.writeFederation(ctx, w)
}

func fedRank(level string) int { return slices.Index(fedLevels, level) }

func (s *Server) handleFederationLevel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Level string `json:"level"`
	}
	if !decodeStrict(w, r, &req) || fedRank(req.Level) < 0 {
		s.writeError(w, http.StatusBadRequest, `Body must be {"level": "closed" | "manual" | "curated"}`)
		return
	}
	s.fedChange(w, r, "matrix.federation_level", req.Level, func(st fedState) fedPlan {
		if st.Level == req.Level {
			return fedPlan{outcome: "unchanged"}
		}
		return fedPlan{widens: fedRank(req.Level) > fedRank(st.Level), outcome: "changed from " + st.Level,
			save: func(ctx context.Context) error { return s.store.Settings().SetSetting(ctx, fedLevelKey, req.Level) }}
	})
}

// normalizeServerName lowercases and trims, then refuses anything Synapse's allow-list and our
// renderer would not take, and this server itself.
func (s *Server) normalizeServerName(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if err := matrixinit.ValidServerName(name); err != nil {
		return "", errors.New("enter a server name like partner.example (no https://, path or port)")
	}
	if _, err := netip.ParseAddr(name); err == nil {
		return "", errors.New("enter a server name, not an IP address")
	}
	if name == s.config.Matrix.ServerName {
		return "", errors.New("that is this server's own name")
	}
	return name, nil
}

func (s *Server) handleFederationAddServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if !decodeStrict(w, r, &req) {
		s.writeError(w, http.StatusBadRequest, `Body must be {"name": "partner.example"}`)
		return
	}
	name, nerr := s.normalizeServerName(req.Name)
	s.fedChange(w, r, "matrix.federation_server", clipTo(strings.TrimSpace(req.Name), 253), func(st fedState) fedPlan {
		switch {
		case nerr != nil:
			return fedPlan{outcome: "refused: " + nerr.Error(), status: http.StatusBadRequest}
		case slices.Contains(st.Servers, name):
			return fedPlan{outcome: "unchanged"}
		case len(st.Servers)+len(st.Presets) >= matrixinit.MaxFederationServers:
			return fedPlan{outcome: "refused: at most 100 servers", status: http.StatusConflict}
		}
		return fedPlan{widens: true, outcome: "added",
			save: func(ctx context.Context) error { return s.saveFedList(ctx, fedServersKey, append(st.Servers, name)) }}
	})
}

func (s *Server) handleFederationRemoveServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.fedChange(w, r, "matrix.federation_server", clipTo(name, 253), func(st fedState) fedPlan {
		if !slices.Contains(st.Servers, name) {
			return fedPlan{outcome: "refused: not on the list", status: http.StatusNotFound}
		}
		rest := slices.DeleteFunc(st.Servers, func(n string) bool { return n == name })
		return fedPlan{outcome: "removed", save: func(ctx context.Context) error { return s.saveFedList(ctx, fedServersKey, rest) }}
	})
}

func (s *Server) handleFederationPreset(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ticked *bool `json:"ticked"`
	}
	if !decodeStrict(w, r, &req) || req.Ticked == nil {
		s.writeError(w, http.StatusBadRequest, `Body must be {"ticked": true | false}`)
		return
	}
	name := r.PathValue("name")
	s.fedChange(w, r, "matrix.federation_preset", clipTo(name, 253), func(st fedState) fedPlan {
		had := slices.Contains(st.Presets, name)
		switch {
		case presetByName(name) == nil:
			return fedPlan{outcome: "refused: not a listed public server", status: http.StatusNotFound}
		case had == *req.Ticked:
			return fedPlan{outcome: "unchanged"}
		case !*req.Ticked:
			rest := slices.DeleteFunc(st.Presets, func(n string) bool { return n == name })
			return fedPlan{outcome: "unticked", save: func(ctx context.Context) error { return s.saveFedList(ctx, fedPresetsKey, rest) }}
		case len(st.Servers)+len(st.Presets) >= matrixinit.MaxFederationServers:
			return fedPlan{outcome: "refused: at most 100 servers", status: http.StatusConflict}
		}
		return fedPlan{widens: true, outcome: "ticked",
			save: func(ctx context.Context) error { return s.saveFedList(ctx, fedPresetsKey, append(st.Presets, name)) }}
	})
}

// handleMatrixServerWellKnown delegates federation to the Synapse host on 443 while federation
// is open; closed, it is absent, as before federation existed.
func (s *Server) handleMatrixServerWellKnown(w http.ResponseWriter, r *http.Request) {
	publicDiscovery(w, r)
	st, err := s.fedState(r.Context())
	u, uerr := url.Parse(s.config.Matrix.Host)
	if !s.config.Matrix.Enabled() || err != nil || uerr != nil || st.Level == fedClosed {
		s.writeError(w, http.StatusNotFound, "Federation is not enabled")
		return
	}
	host := u.Host
	if u.Port() == "" {
		host += ":443"
	}
	s.writeJSON(w, http.StatusOK, map[string]string{"m.server": host})
}

// Federation statuses, from a remote profile lookup through our Synapse (Synapse 1.162,
// measured in the plan's Task 0): 404 M_NOT_FOUND connected; 403 M_FORBIDDEN "Federation
// denied" from either allow-list, including ours before Synapse restarts; 404 M_UNRECOGNIZED
// the peer serves no federation; 502 unreachable, a private address included. The two denials
// differ only in message text, which is not matched.
const (
	fedConnected   = "connected"
	fedNotAllowed  = "not_allowed"
	fedOff         = "federation_off"
	fedUnreachable = "unreachable"
	fedUnknown     = "unknown"
)

// fedProbeTimeout covers Synapse's slowest observed failure (6 s to an absent host).
const fedProbeTimeout = 10 * time.Second

// The status route's phases, each bounded: the console account checks (fedSetupBudget), the
// detached mint and revoke (matrixsync.ConsoleCallTimeout each), the parallel probes on their
// own fresh context (fedProbeTimeout) and writing the answer. The write deadline is their sum,
// past the listener's 15 s, so slow setup never eats the probes' time or the answer.
const (
	fedSetupBudget        = 20 * time.Second
	fedStatusWriteTimeout = fedSetupBudget + 2*matrixsync.ConsoleCallTimeout + fedProbeTimeout + 5*time.Second
)

// fedProbeUser is looked up on each peer; it need not exist there.
const fedProbeUser = "@kymessages-federation-check:"

// handleFederationStatus probes every server on the effective allow-list in parallel with one
// console session. Separate from the settings view so a dead peer never slows the page.
func (s *Server) handleFederationStatus(w http.ResponseWriter, r *http.Request) {
	if !s.matrixOn(w) {
		return
	}
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(fedStatusWriteTimeout))
	st, err := s.fedState(r.Context())
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not read federation settings")
		return
	}
	out := map[string]string{}
	if allow := st.allow(); len(allow) > 0 {
		var mu sync.Mutex
		ctx, cancel := context.WithTimeout(r.Context(), fedSetupBudget)
		defer cancel()
		err = s.mas.AsConsole(ctx, func(ctx context.Context, token string) error {
			ctx = context.WithoutCancel(ctx) // probes get their full fedProbeTimeout after setup
			var wg sync.WaitGroup
			for _, name := range allow {
				wg.Go(func() {
					status := s.probeFederation(ctx, token, name)
					mu.Lock()
					out[name] = status
					mu.Unlock()
				})
			}
			wg.Wait()
			return nil
		})
		if err != nil {
			s.matrixError(w, err)
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, map[string]any{"statuses": out})
}

func (s *Server) probeFederation(ctx context.Context, token, name string) string {
	ctx, cancel := context.WithTimeout(ctx, fedProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.matrixTargets.Synapse+"/_matrix/client/v3/profile/"+url.PathEscape(fedProbeUser+name), nil)
	if err != nil {
		return fedUnknown
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := s.probeHTTP.Do(req)
	if err != nil {
		return fedUnknown
	}
	defer resp.Body.Close()
	var body struct {
		Errcode string `json:"errcode"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body)
	switch {
	case resp.StatusCode == http.StatusOK, resp.StatusCode == http.StatusNotFound && body.Errcode == "M_NOT_FOUND":
		return fedConnected
	case resp.StatusCode == http.StatusForbidden && body.Errcode == "M_FORBIDDEN":
		return fedNotAllowed
	case resp.StatusCode == http.StatusNotFound && body.Errcode == "M_UNRECOGNIZED":
		return fedOff
	case resp.StatusCode == http.StatusBadGateway:
		return fedUnreachable
	}
	return fedUnknown
}

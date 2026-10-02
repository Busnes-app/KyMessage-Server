package api

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/branding"
	"github.com/Busnes-app/ky_server_base/internal/store"
)

// Settings behind the console's branding; brand_logo holds the normalised PNG in base64.
const (
	brandNameKey = "brand_name"
	brandLogoKey = "brand_logo"
)

// brandTimeout bounds a branding change's store writes, Element patch and audit row. They run
// detached, so a dropped connection cannot save a change without its audit row.
const brandTimeout = 10 * time.Second

// effectiveName is the admin's brand_name, else KY_APP_NAME. Display only: capsules, pairing
// and local copies keep KY_APP_NAME, which KyRecovery and restores match byte for byte.
func (s *Server) effectiveName(ctx context.Context) (string, error) {
	v, err := s.store.Settings().GetSetting(ctx, brandNameKey)
	if errors.Is(err, store.ErrNotFound) {
		return s.config.Server.AppName, nil
	}
	return cmp.Or(v, s.config.Server.AppName), err
}

// logo returns the stored PNG, or nil when none is set.
func (s *Server) logo(ctx context.Context) ([]byte, error) {
	v, err := s.store.Settings().GetSetting(ctx, brandLogoKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return base64.StdEncoding.DecodeString(v)
}

func (s *Server) elementConfig() string {
	return filepath.Join(s.config.Matrix.Dir, "element", "config.json")
}

// ReconcileBrand sets Element's brand to the effective name. brandMu makes it the file's only
// writer in this process: the name handler and cmd/server's maintenance tick both call it, and
// two in-place writers could interleave into invalid JSON. It writes only on change, never
// creates the file, and logs a failure once per streak. No-op without Matrix.
func (s *Server) ReconcileBrand(ctx context.Context) error {
	if !s.config.Matrix.Enabled() {
		return nil
	}
	s.brandMu.Lock()
	defer s.brandMu.Unlock()
	name, err := s.effectiveName(ctx)
	if err == nil {
		var changed bool
		if changed, err = branding.PatchElementBrand(s.elementConfig(), name); changed {
			log.Printf("[BRANDING] Element brand set to %q", name)
		}
	}
	switch {
	case err != nil && !s.brandFailing:
		log.Printf("[BRANDING] Element brand not updated (retrying every minute): %v", err)
	case err == nil && s.brandFailing:
		log.Printf("[BRANDING] Element brand updated again")
	}
	s.brandFailing, s.brandErr = err != nil, ""
	if err != nil {
		s.brandErr = err.Error()
	}
	return err
}

type logoView struct {
	Custom bool   `json:"custom"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

// elementView is what Element's config.json says now; Error is why it differs or is unreadable.
// Served is the brand Element serves: its image copies config.json at container start, so a
// rename reaches it only after a restart, whose command is RestartHint
// (KY_MATRIX_ELEMENT_RESTART_HINT). ServedError is why Served is unknown.
type elementView struct {
	Brand       *string `json:"brand,omitempty"`
	Error       string  `json:"error,omitempty"`
	Served      *string `json:"served,omitempty"`
	ServedError string  `json:"served_error,omitempty"`
	RestartHint string  `json:"restart_hint"`
}

type brandingView struct {
	Name        string       `json:"name"`
	StoredName  string       `json:"stored_name"`
	DefaultName string       `json:"default_name"`
	Logo        logoView     `json:"logo"`
	Element     *elementView `json:"element,omitempty"` // only with Matrix
}

func (s *Server) brandingState(ctx context.Context) (brandingView, error) {
	stored, err := s.store.Settings().GetSetting(ctx, brandNameKey)
	if errors.Is(err, store.ErrNotFound) {
		stored, err = "", nil
	}
	if err != nil {
		return brandingView{}, err
	}
	v := brandingView{Name: cmp.Or(stored, s.config.Server.AppName), StoredName: stored, DefaultName: s.config.Server.AppName}
	png, err := s.logo(ctx)
	if err != nil {
		return brandingView{}, err
	}
	if png != nil {
		sum := sha256.Sum256(png)
		v.Logo = logoView{Custom: true, SHA256: hex.EncodeToString(sum[:]), Size: len(png)}
	}
	if s.config.Matrix.Enabled() {
		e := &elementView{RestartHint: s.config.Matrix.ElementRestartHint}
		if brand, err := branding.ElementBrand(s.elementConfig()); err != nil {
			e.Error = err.Error()
		} else {
			e.Brand = &brand
		}
		s.brandMu.Lock()
		if e.Brand != nil && *e.Brand != v.Name {
			e.Error = s.brandErr
		}
		s.brandMu.Unlock()
		if served, err := s.servedBrand(ctx); err != nil {
			e.ServedError = err.Error()
		} else {
			e.Served = &served
		}
		v.Element = e
	}
	return v, nil
}

// servedBrand fetches the brand Element serves, on the internal network with the probes'
// client (no proxy, no redirects) and timeout.
func (s *Server) servedBrand(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.matrixTargets.Element+"/config.json", nil)
	if err != nil {
		return "", err
	}
	resp, err := s.probeHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Element answered HTTP %d for /config.json", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return "", err
	}
	v, err := branding.ParseBrand(b)
	if err != nil {
		return "", fmt.Errorf("Element's /config.json: %w", err)
	}
	return v, nil
}

func (s *Server) writeBranding(ctx context.Context, w http.ResponseWriter) {
	v, err := s.brandingState(ctx)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not read branding")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	s.writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	s.writeBranding(r.Context(), w)
}

// handleBrandName saves the product name (blank resets it to KY_APP_NAME), then patches
// Element. An Element failure leaves the name saved; the answer shows what Element says.
func (s *Server) handleBrandName(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name *string `json:"name"`
	}
	if !decodeStrict(w, r, &req) || req.Name == nil {
		s.writeError(w, http.StatusBadRequest, `Body must be {"name": "..."}`)
		return
	}
	actor := s.actorID(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), brandTimeout)
	defer cancel()
	old, _ := s.effectiveName(ctx) // audit detail only
	record := func(outcome, name string) {
		// 40 bytes each: %q can double a name, and details are capped at 200 bytes.
		s.audit(ctx, actor, r, "admin.brand_name", "", auditFields(outcome, "old", clipTo(old, 40), "new", clipTo(name, 40)))
	}
	name := ""
	if strings.TrimSpace(*req.Name) != "" {
		v, err := branding.ValidateName(*req.Name)
		if err != nil {
			record("refused: "+err.Error(), "")
			s.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		name = v
	}
	var err error
	if name == "" {
		err = s.store.Settings().DeleteSetting(ctx, brandNameKey)
	} else {
		err = s.store.Settings().SetSetting(ctx, brandNameKey, name)
	}
	if err != nil {
		record("error: "+err.Error(), name)
		s.writeError(w, http.StatusInternalServerError, "Could not save the name")
		return
	}
	outcome := "saved"
	if err := s.ReconcileBrand(ctx); err != nil {
		outcome = "saved; Element not updated: " + err.Error()
	}
	record(outcome, cmp.Or(name, s.config.Server.AppName))
	s.writeBranding(ctx, w)
}

// handleBrandLogo stores an uploaded PNG, normalised: decoded and re-encoded so no ancillary
// chunk survives. Only image/png (SVG can carry script). Refusals are audited, never the bytes.
func (s *Server) handleBrandLogo(w http.ResponseWriter, r *http.Request) {
	actor := s.actorID(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), brandTimeout)
	defer cancel()
	record := func(outcome string, kv ...string) {
		s.audit(ctx, actor, r, "admin.brand_logo", "", auditFields(outcome, kv...))
	}
	refuse := func(status int, reason string) {
		record("refused: " + reason)
		s.writeError(w, status, reason)
	}
	if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "image/png" {
		refuse(http.StatusUnsupportedMediaType, "the logo must be a PNG image (image/png)")
		return
	}
	// Its own cap, not only ServeHTTP's: this is the logo's limit, whatever the API's becomes.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, branding.MaxLogoBytes))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		refuse(http.StatusRequestEntityTooLarge, branding.ErrLogoTooLarge.Error())
		return
	}
	if err != nil {
		refuse(http.StatusBadRequest, "the upload could not be read")
		return
	}
	png, err := branding.NormalizePNG(body)
	if errors.Is(err, branding.ErrLogoTooLarge) {
		refuse(http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	if err != nil {
		refuse(http.StatusBadRequest, err.Error())
		return
	}
	sum := sha256.Sum256(png)
	digest, size := hex.EncodeToString(sum[:]), strconv.Itoa(len(png))
	if err := s.store.Settings().SetSetting(ctx, brandLogoKey, base64.StdEncoding.EncodeToString(png)); err != nil {
		record("error: "+err.Error(), "sha256", digest, "size", size)
		s.writeError(w, http.StatusInternalServerError, "Could not save the logo")
		return
	}
	record("saved", "sha256", digest, "size", size)
	s.writeBranding(ctx, w)
}

// handleBrandLogoReset returns /app-icon.png to the embedded stamp.
func (s *Server) handleBrandLogoReset(w http.ResponseWriter, r *http.Request) {
	actor := s.actorID(r)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), brandTimeout)
	defer cancel()
	outcome := "reset"
	err := s.store.Settings().DeleteSetting(ctx, brandLogoKey)
	if err != nil {
		outcome = "error: " + err.Error()
	}
	s.audit(ctx, actor, r, "admin.brand_logo", "", auditFields(outcome))
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, "Could not reset the logo")
		return
	}
	s.writeBranding(ctx, w)
}

// appIcon serves the admin's logo at /app-icon.png, else the embedded stamp. Public: the login
// page and Element's sign-in page show it. no-cache with an ETag: browsers revalidate on every
// load, so a new logo or a reset shows at once without re-downloading an unchanged one.
func (s *Server) appIcon(static http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		png, err := s.logo(r.Context())
		if err != nil || png == nil {
			static.ServeHTTP(w, r)
			return
		}
		sum := sha256.Sum256(png)
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
		http.ServeContent(w, r, "app-icon.png", time.Time{}, bytes.NewReader(png))
	}
}

// clipTo bounds v to n bytes of valid UTF-8.
func clipTo(v string, n int) string {
	if len(v) <= n {
		return v
	}
	return strings.ToValidUTF8(v[:n], "")
}

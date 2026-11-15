package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Busnes-app/ky-primitives/suitedir"

	"github.com/Busnes-app/ky_server_base/internal/config"
	"github.com/Busnes-app/ky_server_base/internal/kycalendar"
	"github.com/Busnes-app/ky_server_base/internal/matrixinit"
)

// Timing of the KyIdentity lookup, as KyCalendar's KyPost link. Variables so tests can
// shorten them.
var (
	kycalFirstWait = 5 * time.Second
	kycalRefresh   = time.Hour   // a working pair is fetched again this often
	kycalRetry     = time.Minute // first retry after a failure, doubling up to kycalRefresh
	kycalMinGap    = time.Minute // a kick (a refused signature) waits this long after the last lookup
)

// Why a suite pair is missing (pairView.Reason).
const (
	pairPending       = "pending"        // not looked up yet
	pairMatrixOff     = "matrix_off"     // no Matrix stack: never looked up
	pairNoKyIdentity  = "no_kyidentity"  // suite sign-in is off
	pairNoPeer        = "no_peer"        // KyIdentity has no such app paired with KyMessages
	pairUnauthorized  = "unauthorized"   // KyIdentity refused the console's client credentials
	pairUnreachable   = "unreachable"    // KyIdentity did not answer usefully
	pairInvalidPeer   = "invalid_peer"   // KyIdentity's address for the app is not an https origin
	pairNoCredentials = "no_credentials" // the sign-in client secret could not be read
)

// suitePair is one suite app paired with KyMessages (KyCalendar, KyPost): the environment's
// pair when it names one, else KyIdentity's, kept fresh. The secret lives only in Link.
type suitePair struct {
	kycalendar.Link
	client string // suitedir client ID, also the console route and log name
	env    config.PairConfig
	kick   chan struct{}

	mu      sync.Mutex
	reason  string    // "" while paired
	checked time.Time // the last lookup; zero for an environment pair
}

func newSuitePair(client string, env config.PairConfig, matrix bool) *suitePair {
	p := &suitePair{client: client, env: env, kick: make(chan struct{}, 1), reason: pairPending}
	switch {
	case !matrix:
		p.reason = pairMatrixOff
	case env.URL != "":
		p.Set(&kycalendar.Peer{URL: env.URL, Secret: env.Secret, Source: kycalendar.SourceEnvironment})
		p.reason = ""
	}
	return p
}

func (p *suitePair) set(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reason, p.checked = reason, time.Now()
}

func (p *suitePair) get() (string, time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reason, p.checked
}

// kickNow asks for a lookup soon: the peer signed with a secret we do not hold.
func (p *suitePair) kickNow() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

// StartSuitePairs pairs KyCalendar and KyPost through KyIdentity where the environment names
// none and Matrix is on: it waits up to kycalFirstWait for the first lookups, then keeps the
// pairs fresh until ctx ends. cmd/server calls it before serving.
func (s *Server) StartSuitePairs(ctx context.Context) {
	if !s.config.Matrix.Enabled() {
		return
	}
	first, cancel := context.WithTimeout(ctx, kycalFirstWait)
	defer cancel()
	var wg sync.WaitGroup
	for _, p := range []*suitePair{s.kycal, s.kypost} {
		if p.env.URL == "" {
			wg.Go(func() {
				ok := s.resolvePair(first, p)
				go s.pairLoop(ctx, p, ok)
			})
		}
	}
	wg.Wait()
}

// kickKyCalendar asks for a KyCalendar lookup soon.
func (s *Server) kickKyCalendar() { s.kycal.kickNow() }

func (s *Server) pairLoop(ctx context.Context, p *suitePair, ok bool) {
	last, failures := time.Now(), 0
	if !ok {
		failures = 1
	}
	for {
		wait := kycalRefresh
		if failures > 0 {
			wait = min(kycalRetry<<min(failures-1, 16), kycalRefresh)
		}
		next := last.Add(wait)
	sleep:
		for {
			t := time.NewTimer(time.Until(next))
			select {
			case <-ctx.Done():
				t.Stop()
				return
			case <-t.C:
				break sleep
			case <-p.kick:
				t.Stop()
				// No sooner than kycalMinGap after the last lookup, so refused signatures
				// cannot hammer KyIdentity.
				if soon := last.Add(kycalMinGap); soon.Before(next) {
					next = soon
				}
			}
		}
		last = time.Now()
		if s.resolvePair(ctx, p) {
			failures = 0
		} else {
			failures++
		}
	}
}

func (s *Server) resolveKyCalendar(ctx context.Context) bool { return s.resolvePair(ctx, s.kycal) }

// resolvePair asks KyIdentity for p's app and pairs it, reporting whether the lookup worked. A
// refusal unpairs; an unreachable KyIdentity keeps a pair it issued, so a KyIdentity restart
// does not drop the pair. The secret is never logged.
func (s *Server) resolvePair(ctx context.Context, p *suitePair) bool {
	dir, err := s.kyidentity.SuiteDirectory()
	if err != nil {
		return p.unpair(pairNoCredentials, err)
	}
	if dir == nil {
		return p.unpair(pairNoKyIdentity, nil)
	}
	peer, err := dir.Peer(ctx, p.client)
	switch {
	case errors.Is(err, suitedir.ErrNoPeer):
		return p.unpair(pairNoPeer, nil)
	case errors.Is(err, suitedir.ErrUnauthorized):
		return p.unpair(pairUnauthorized, err)
	case err != nil:
		if p.Peer() != nil {
			log.Printf("[%s] KyIdentity lookup failed, keeping the current pair: %v", strings.ToUpper(p.client), err)
			return false
		}
		return p.unpair(pairUnreachable, err)
	}
	u, err := matrixinit.Origin(peer.URL)
	if err != nil {
		return p.unpair(pairInvalidPeer, nil)
	}
	if cur := p.Peer(); cur == nil || cur.URL != u || string(cur.Secret) != string(peer.Secret) {
		log.Printf("[%s] paired through KyIdentity: %s", strings.ToUpper(p.client), u)
	}
	p.Set(&kycalendar.Peer{URL: u, Secret: peer.Secret, Source: kycalendar.SourceKyIdentity})
	p.set("")
	return true
}

// unpair drops the pair for reason, logging once per change of reason.
func (p *suitePair) unpair(reason string, err error) bool {
	if prev, _ := p.get(); prev != reason {
		if err != nil {
			log.Printf("[%s] not paired through KyIdentity (%s): %v", strings.ToUpper(p.client), reason, err)
		} else {
			log.Printf("[%s] not paired through KyIdentity (%s)", strings.ToUpper(p.client), reason)
		}
	}
	p.Set(nil)
	p.set(reason)
	return false
}

// pairView is the console's row for a pair. It never carries the secret.
type pairView struct {
	Connected bool       `json:"connected"`
	Source    string     `json:"source,omitempty"` // environment or kyidentity
	URL       string     `json:"url,omitempty"`
	Reason    string     `json:"reason,omitempty"` // why not connected
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

// handlePairStatus answers GET /api/admin/kycalendar and /api/admin/kypost.
func (s *Server) handlePairStatus(p *suitePair) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var v pairView
		reason, checked := p.get()
		if !checked.IsZero() {
			v.CheckedAt = &checked
		}
		if peer := p.Peer(); peer != nil {
			v.Connected, v.Source, v.URL = true, peer.Source, peer.URL
		} else {
			v.Reason = reason
		}
		w.Header().Set("Cache-Control", "no-store")
		s.writeJSON(w, http.StatusOK, v)
	}
}

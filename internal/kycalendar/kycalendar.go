// Package kycalendar is the one definition of what KyCalendar sends KyMessages about meeting
// rooms (docs/KYCALENDAR.md), and the live pair it is verified under. Every request is signed
// with ky-primitives/syncauth (scheme v1) under the pair secret; each route has its own event
// type, so a request cannot be replayed onto the other route.
package kycalendar

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/Busnes-app/ky-primitives/syncauth"
)

// Event types, one per route.
const (
	EventPut   = "kycalendar.meeting.put"   // PUT /api/meetings/{id}
	EventClose = "kycalendar.meeting.close" // DELETE /api/meetings/{id}
	// MaxBody bounds a request: 100 subjects and a title fit with room to spare.
	MaxBody    = 64 << 10
	MaxMembers = 100
	MaxTitle   = 255
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// ValidID reports whether id is a KyCalendar meeting ID.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// PutRequest creates or reconciles a meeting room. Guests belong to the unbuilt guest half:
// a non-empty list is refused. Other fields (windows) are ignored.
type PutRequest struct {
	Sub        string            `json:"sub"`
	Issuer     string            `json:"issuer"`
	Title      string            `json:"title"`
	MemberSubs []string          `json:"member_subs"`
	Guests     []json.RawMessage `json:"guests"`
}

// ErrBadBody is a malformed request; ErrGuests one that names guests.
var (
	ErrBadBody = errors.New("kycalendar: bad body")
	ErrGuests  = errors.New("kycalendar: guests are not supported")
)

// DecodePut parses and checks a PUT body.
func DecodePut(body []byte) (PutRequest, error) {
	var p PutRequest
	if err := json.Unmarshal(body, &p); err != nil {
		return p, ErrBadBody
	}
	if len(p.Guests) > 0 {
		return p, ErrGuests
	}
	if !validText(p.Sub, 255) || !validText(p.Issuer, 512) || !validText(p.Title, MaxTitle) || len(p.MemberSubs) > MaxMembers {
		return p, ErrBadBody
	}
	for _, s := range p.MemberSubs {
		if !validText(s, 255) {
			return p, ErrBadBody
		}
	}
	return p, nil
}

// validText is 1 to max bytes of UTF-8 without control characters.
func validText(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

// Verify reads a request's body and checks it against want (the route's event type), the
// secret and replay. Replayed reports a valid signature on an event already accepted. Any
// other syncauth error is a refused signature; an error from the guard itself comes back
// wrapped as it is, so the caller can tell an outage from a forgery.
func Verify(r *http.Request, secret []byte, want string, replay syncauth.Replay) (body []byte, ev syncauth.Event, replayed bool, err error) {
	body, err = io.ReadAll(io.LimitReader(r.Body, MaxBody+1))
	if err != nil {
		return nil, ev, false, err
	}
	if len(body) > MaxBody {
		return nil, ev, false, syncauth.ErrBodyTooLarge
	}
	h := syncauth.FromRequest(r)
	if h.EventType != want {
		return nil, ev, false, syncauth.ErrBadSignature
	}
	ev, err = syncauth.Verify(secret, h, body, syncauth.Options{Replay: replay})
	if errors.Is(err, syncauth.ErrReplay) {
		return nil, ev, true, nil
	}
	return body, ev, false, err
}

// Where a Peer came from.
const (
	SourceEnvironment = "environment" // KY_KYCALENDAR_URL and _SECRET_FILE
	SourceKyIdentity  = "kyidentity"  // KyIdentity's suite directory
)

// Peer is the paired KyCalendar: its https origin and the shared secret.
type Peer struct {
	URL    string
	Secret []byte
	Source string
}

// Link holds the live pair, which KyIdentity can replace while the server runs. The secret
// lives only here, in memory.
type Link struct{ peer atomic.Pointer[Peer] }

// Peer is the paired KyCalendar, nil when none.
func (l *Link) Peer() *Peer { return l.peer.Load() }

// Set pairs p; nil unpairs.
func (l *Link) Set(p *Peer) { l.peer.Store(p) }

// Package presence is the one definition of the suite status contract (docs/PRESENCE.md):
// what KyCalendar and KyPost send KyMessages, and how a status is worked out from it.
// Bodies are decoded strictly: an unknown field (an event title, a reply text) is refused.
package presence

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

// Event types, one per route and pair.
const (
	EventBusy             = "kycalendar.presence.busy"   // POST /api/suite/presence/calendar
	EventMe               = "kycalendar.presence.me"     // POST /api/suite/presence/me
	EventOOO              = "kypost.presence.ooo"        // POST /api/suite/presence/out-of-office
	EventLookupKyCalendar = "kycalendar.presence.lookup" // POST /api/suite/presence/lookup
	EventLookupKyPost     = "kypost.presence.lookup"
)

// Limits.
const (
	MaxBusy   = 200
	BusyAhead = 49 * time.Hour // KyCalendar sends 48 hours
	MaxLookup = 50
	MaxManual = 7 * 24 * time.Hour
	MaxOOO    = 366 * 24 * time.Hour
	// A calendar push older than this no longer says someone is available.
	Fresh = 24 * time.Hour
)

// States.
const (
	Available   = "available"
	Busy        = "busy"
	InMeeting   = "in_a_meeting"
	Away        = "away"
	OutOfOffice = "out_of_office"
	Unknown     = "unknown"
)

var (
	ErrBadBody = errors.New("presence: bad body")
	ErrTooMany = errors.New("presence: too many people")
)

// Window is one busy block, RFC 3339 at offset zero, [Start, End).
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// User names the user a request is about.
type User struct {
	Sub    string `json:"sub"`
	Issuer string `json:"issuer"`
}

// BusyPush is KyCalendar's whole busy list for one user.
type BusyPush struct {
	User
	Revision int64    `json:"revision"`
	Busy     []Window `json:"busy"`
}

// OOOPush is KyPost's out-of-office state for one user.
type OOOPush struct {
	User
	Revision int64      `json:"revision"`
	Away     bool       `json:"away"`
	Start    *time.Time `json:"start"`
	End      *time.Time `json:"end"`
}

// MeRequest reads, and optionally changes, the user's own switch and manual status. Manual
// absent leaves it; null clears it.
type MeRequest struct {
	User
	Share  *bool           `json:"share"`
	Manual json.RawMessage `json:"manual"`
}

// Manual is a status the user chose, until it expires.
type Manual struct {
	State string    `json:"state"`
	Until time.Time `json:"until"`
}

// LookupRequest asks for up to MaxLookup people's status.
type LookupRequest struct {
	User
	Subs []string `json:"subs"`
}

// strict decodes exactly one JSON object with no unknown fields.
func strict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil || dec.Decode(&struct{}{}) != io.EOF {
		return ErrBadBody
	}
	return nil
}

// Text is 1 to max bytes of UTF-8 without control characters.
func Text(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func (u User) valid() bool { return Text(u.Sub, 255) && Text(u.Issuer, 512) }

func utc(t time.Time) bool {
	_, off := t.Zone()
	return off == 0 && !t.IsZero()
}

// DecodeBusy checks a push; blocks that already ended are dropped, the rest sorted.
func DecodeBusy(body []byte, now time.Time) (BusyPush, error) {
	var p BusyPush
	if strict(body, &p) != nil || !p.valid() || p.Revision <= 0 || p.Busy == nil || len(p.Busy) > MaxBusy {
		return p, ErrBadBody
	}
	kept := p.Busy[:0]
	for _, w := range p.Busy {
		if !utc(w.Start) || !utc(w.End) || !w.Start.Before(w.End) || w.End.After(now.Add(BusyAhead)) {
			return p, ErrBadBody
		}
		if w.End.After(now) {
			kept = append(kept, w)
		}
	}
	slices.SortFunc(kept, func(a, b Window) int { return a.Start.Compare(b.Start) })
	p.Busy = kept
	return p, nil
}

// DecodeOOO checks a push. Away false ignores the period.
func DecodeOOO(body []byte) (OOOPush, error) {
	var p OOOPush
	if strict(body, &p) != nil || !p.valid() || p.Revision <= 0 {
		return p, ErrBadBody
	}
	if !p.Away {
		p.Start, p.End = nil, nil
		return p, nil
	}
	if p.Start == nil || p.End == nil || !utc(*p.Start) || !utc(*p.End) || !p.Start.Before(*p.End) || p.End.Sub(*p.Start) > MaxOOO {
		return p, ErrBadBody
	}
	return p, nil
}

// DecodeMe checks a request; manual is nil when absent or null, and clear reports null.
func DecodeMe(body []byte, now time.Time) (req MeRequest, manual *Manual, clear bool, err error) {
	if strict(body, &req) != nil || !req.valid() {
		return req, nil, false, ErrBadBody
	}
	switch {
	case req.Manual == nil:
	case string(req.Manual) == "null":
		clear = true
	default:
		var m Manual
		if strict(req.Manual, &m) != nil || (m.State != Available && m.State != Busy && m.State != Away) ||
			!utc(m.Until) || !m.Until.After(now) || m.Until.After(now.Add(MaxManual)) {
			return req, nil, false, ErrBadBody
		}
		manual = &m
	}
	if manual != nil && req.Share != nil && !*req.Share {
		return req, nil, false, ErrBadBody
	}
	return req, manual, clear, nil
}

// DecodeLookup checks a request: ErrTooMany over MaxLookup people.
func DecodeLookup(body []byte) (LookupRequest, error) {
	var req LookupRequest
	if strict(body, &req) != nil || !req.valid() || req.Subs == nil {
		return req, ErrBadBody
	}
	if len(req.Subs) > MaxLookup {
		return req, ErrTooMany
	}
	for _, s := range req.Subs {
		if !Text(s, 255) {
			return req, ErrBadBody
		}
	}
	return req, nil
}

// Status works out a member's status at now from their stored inputs (ok false: none).
// Until is set for in_a_meeting and out_of_office only.
func Status(p store.Presence, ok bool, now time.Time) (state string, until time.Time) {
	switch {
	case !ok || p.ShareOff:
		return Unknown, time.Time{}
	case p.ManualState != "" && now.Before(p.ManualUntil):
		return p.ManualState, time.Time{}
	case !p.OOOStart.IsZero() && !now.Before(p.OOOStart) && now.Before(p.OOOEnd):
		return OutOfOffice, p.OOOEnd
	}
	if end, in := meetingEnd(p.Busy, now); in {
		return InMeeting, end
	}
	if !p.CalendarAt.IsZero() && now.Sub(p.CalendarAt) < Fresh {
		return Available, time.Time{}
	}
	return Unknown, time.Time{}
}

// meetingEnd is the end of the run of blocks, overlapping or touching, that holds now. Busy
// is sorted by start.
func meetingEnd(busy []store.MeetingWindow, now time.Time) (time.Time, bool) {
	var end time.Time
	in := false
	for _, w := range busy {
		switch {
		case in && !w.Start.After(end):
			end = later(end, w.End)
		case in:
			return end, true
		case !now.Before(w.Start) && now.Before(w.End):
			in, end = true, w.End
		}
	}
	return end, in
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

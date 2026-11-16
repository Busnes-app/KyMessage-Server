package presence

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky_server_base/internal/store"
)

var now = time.Date(2026, 11, 16, 14, 0, 0, 0, time.UTC)

func ts(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }

func TestDecodeBusy(t *testing.T) {
	ok := fmt.Sprintf(`{"sub":"s","issuer":"https://id","revision":1,"busy":[{"start":%q,"end":%q},{"start":%q,"end":%q}]}`,
		ts(time.Hour), ts(2*time.Hour), ts(-2*time.Hour), ts(-time.Hour))
	p, err := DecodeBusy([]byte(ok), now)
	if err != nil || len(p.Busy) != 1 || !p.Busy[0].Start.Equal(now.Add(time.Hour)) {
		t.Fatalf("ok: %+v %v", p, err)
	}
	for name, body := range map[string]string{
		"title":        `{"sub":"s","issuer":"https://id","revision":1,"busy":[],"title":"Board meeting"}`,
		"attendees":    fmt.Sprintf(`{"sub":"s","issuer":"https://id","revision":1,"busy":[{"start":%q,"end":%q,"attendees":["a"]}]}`, ts(0), ts(time.Hour)),
		"no busy":      `{"sub":"s","issuer":"https://id","revision":1}`,
		"no revision":  `{"sub":"s","issuer":"https://id","busy":[]}`,
		"no sub":       `{"issuer":"https://id","revision":1,"busy":[]}`,
		"control":      `{"sub":"s\n","issuer":"https://id","revision":1,"busy":[]}`,
		"too far":      fmt.Sprintf(`{"sub":"s","issuer":"https://id","revision":1,"busy":[{"start":%q,"end":%q}]}`, ts(48*time.Hour), ts(50*time.Hour)),
		"backwards":    fmt.Sprintf(`{"sub":"s","issuer":"https://id","revision":1,"busy":[{"start":%q,"end":%q}]}`, ts(time.Hour), ts(0)),
		"offset":       `{"sub":"s","issuer":"https://id","revision":1,"busy":[{"start":"2026-11-16T15:00:00+01:00","end":"2026-11-16T16:00:00+01:00"}]}`,
		"two objects":  `{"sub":"s","issuer":"https://id","revision":1,"busy":[]}{}`,
		"not json":     `busy`,
		"over the cap": `{"sub":"s","issuer":"https://id","revision":1,"busy":[` + strings.Repeat(fmt.Sprintf(`{"start":%q,"end":%q},`, ts(0), ts(time.Hour)), MaxBusy) + fmt.Sprintf(`{"start":%q,"end":%q}]}`, ts(0), ts(time.Hour)),
	} {
		if _, err := DecodeBusy([]byte(body), now); !errors.Is(err, ErrBadBody) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDecodeOOO(t *testing.T) {
	if p, err := DecodeOOO([]byte(fmt.Sprintf(`{"sub":"s","issuer":"i","revision":2,"away":true,"start":%q,"end":%q}`, ts(0), ts(72*time.Hour)))); err != nil || !p.End.Equal(now.Add(72*time.Hour)) {
		t.Fatalf("on: %+v %v", p, err)
	}
	if _, err := DecodeOOO([]byte(`{"sub":"s","issuer":"i","revision":3,"away":false,"start":"x"}`)); !errors.Is(err, ErrBadBody) {
		t.Fatalf("a malformed time is refused even when off: %v", err)
	}
	if p, err := DecodeOOO([]byte(`{"sub":"s","issuer":"i","revision":3,"away":false}`)); err != nil || p.Start != nil {
		t.Fatalf("off: %+v %v", p, err)
	}
	for name, body := range map[string]string{
		"reply text": fmt.Sprintf(`{"sub":"s","issuer":"i","revision":2,"away":true,"start":%q,"end":%q,"comment_local":"I am away"}`, ts(0), ts(time.Hour)),
		"no end":     fmt.Sprintf(`{"sub":"s","issuer":"i","revision":2,"away":true,"start":%q}`, ts(0)),
		"too long":   fmt.Sprintf(`{"sub":"s","issuer":"i","revision":2,"away":true,"start":%q,"end":%q}`, ts(0), ts(367*24*time.Hour)),
		"zero rev":   `{"sub":"s","issuer":"i","revision":0,"away":false}`,
	} {
		if _, err := DecodeOOO([]byte(body)); !errors.Is(err, ErrBadBody) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDecodeMe(t *testing.T) {
	_, m, clear, err := DecodeMe([]byte(`{"sub":"s","issuer":"i"}`), now)
	if err != nil || m != nil || clear {
		t.Fatalf("read: %v %v %v", m, clear, err)
	}
	if _, _, clear, err := DecodeMe([]byte(`{"sub":"s","issuer":"i","manual":null}`), now); err != nil || !clear {
		t.Fatalf("clear: %v %v", clear, err)
	}
	if _, m, _, err := DecodeMe([]byte(fmt.Sprintf(`{"sub":"s","issuer":"i","manual":{"state":"away","until":%q}}`, ts(time.Hour))), now); err != nil || m.State != Away {
		t.Fatalf("set: %v %v", m, err)
	}
	for name, body := range map[string]string{
		"state":       fmt.Sprintf(`{"sub":"s","issuer":"i","manual":{"state":"in_a_meeting","until":%q}}`, ts(time.Hour)),
		"no expiry":   `{"sub":"s","issuer":"i","manual":{"state":"busy"}}`,
		"past":        fmt.Sprintf(`{"sub":"s","issuer":"i","manual":{"state":"busy","until":%q}}`, ts(-time.Minute)),
		"too far":     fmt.Sprintf(`{"sub":"s","issuer":"i","manual":{"state":"busy","until":%q}}`, ts(8*24*time.Hour)),
		"off and set": fmt.Sprintf(`{"sub":"s","issuer":"i","share":false,"manual":{"state":"busy","until":%q}}`, ts(time.Hour)),
		"extra":       fmt.Sprintf(`{"sub":"s","issuer":"i","manual":{"state":"busy","until":%q,"note":"x"}}`, ts(time.Hour)),
	} {
		if _, _, _, err := DecodeMe([]byte(body), now); !errors.Is(err, ErrBadBody) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDecodeLookup(t *testing.T) {
	subs := `"a"` + strings.Repeat(`,"a"`, MaxLookup-1)
	if r, err := DecodeLookup([]byte(`{"sub":"s","issuer":"i","subs":[` + subs + `]}`)); err != nil || len(r.Subs) != MaxLookup {
		t.Fatalf("at the cap: %v", err)
	}
	if _, err := DecodeLookup([]byte(`{"sub":"s","issuer":"i","subs":[` + subs + `,"b"]}`)); !errors.Is(err, ErrTooMany) {
		t.Fatalf("over the cap: %v", err)
	}
	for _, body := range []string{`{"sub":"s","issuer":"i"}`, `{"sub":"s","issuer":"i","subs":[""]}`, `{"sub":"s","issuer":"i","subs":[],"x":1}`} {
		if _, err := DecodeLookup([]byte(body)); !errors.Is(err, ErrBadBody) {
			t.Errorf("%s: %v", body, err)
		}
	}
}

func w(a, b time.Duration) store.MeetingWindow {
	return store.MeetingWindow{Start: now.Add(a), End: now.Add(b)}
}

func TestStatus(t *testing.T) {
	fresh := now.Add(-time.Hour)
	meeting := []store.MeetingWindow{w(-time.Hour, 30*time.Minute), w(30*time.Minute, time.Hour), w(2*time.Hour, 3*time.Hour)}
	for _, c := range []struct {
		name  string
		p     store.Presence
		ok    bool
		state string
		until time.Duration // 0: none
	}{
		{"no row", store.Presence{}, false, Unknown, 0},
		{"no calendar", store.Presence{}, true, Unknown, 0},
		{"stale calendar", store.Presence{CalendarAt: now.Add(-25 * time.Hour)}, true, Unknown, 0},
		{"free", store.Presence{CalendarAt: fresh, Busy: []store.MeetingWindow{w(time.Hour, 2*time.Hour)}}, true, Available, 0},
		{"in back-to-back meetings", store.Presence{CalendarAt: fresh, Busy: meeting}, true, InMeeting, time.Hour},
		{"meeting kept from a stale push", store.Presence{CalendarAt: now.Add(-30 * time.Hour), Busy: meeting}, true, InMeeting, time.Hour},
		{"out of office", store.Presence{CalendarAt: fresh, Busy: meeting, OOOStart: now.Add(-time.Hour), OOOEnd: now.Add(48 * time.Hour)}, true, OutOfOffice, 48 * time.Hour},
		{"out of office later", store.Presence{CalendarAt: fresh, OOOStart: now.Add(time.Hour), OOOEnd: now.Add(48 * time.Hour)}, true, Available, 0},
		{"manual wins", store.Presence{CalendarAt: fresh, Busy: meeting, OOOStart: now.Add(-time.Hour), OOOEnd: now.Add(time.Hour), ManualState: Available, ManualUntil: now.Add(time.Minute)}, true, Available, 0},
		{"manual expired", store.Presence{CalendarAt: fresh, Busy: meeting, ManualState: Away, ManualUntil: now}, true, InMeeting, time.Hour},
		{"opted out", store.Presence{ShareOff: true, CalendarAt: fresh, ManualState: Busy, ManualUntil: now.Add(time.Hour)}, true, Unknown, 0},
	} {
		state, until := Status(c.p, c.ok, now)
		want := time.Time{}
		if c.until != 0 {
			want = now.Add(c.until)
		}
		if state != c.state || !until.Equal(want) {
			t.Errorf("%s: %s %v, want %s %v", c.name, state, until, c.state, want)
		}
	}
}

package kycalendar

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Busnes-app/ky-primitives/syncauth"
)

func TestValidID(t *testing.T) {
	for id, want := range map[string]bool{
		"abcdefghijklmnop": true, strings.Repeat("A", 64): true, "a_b-c0123456789Z": true,
		"abcdefghijklmno": false, strings.Repeat("A", 65): false, "abcdefghijklmno.": false,
		"abcdefghijklmno/": false, "abcdefghijklmnoé": false,
	} {
		if ValidID(id) != want {
			t.Errorf("%q: want %v", id, want)
		}
	}
}

func TestDecodePut(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	ok := `{"sub":"s","issuer":"https://id","title":"T","member_subs":["a"],"guests":[{"key":"gkey_0123456789ab"}],
		"windows":[{"start":"2026-11-14T11:50:00Z","end":"2026-11-14T13:00:00Z"},{"start":"2027-02-12T09:00:00+00:00","end":"2027-02-12T10:00:00Z"}]}`
	p, err := DecodePut([]byte(ok), now)
	if err != nil || p.Title != "T" || len(p.MemberSubs) != 1 || len(p.Guests) != 1 || len(p.Windows) != 2 || !p.Windows[0].End.Equal(now.Add(time.Hour)) {
		t.Fatalf("%+v %v", p, err)
	}
	if p, err := DecodePut([]byte(`{"sub":"s","issuer":"i","title":"T","id":"ignored"}`), now); err != nil || p.Guests != nil {
		t.Fatalf("without guests: %+v %v", p, err)
	}
	many := `["` + strings.Repeat(`a","`, MaxMembers) + `a"]`
	guests := func(n int, key string) string {
		return `{"sub":"s","issuer":"i","title":"T","guests":[` + strings.TrimSuffix(strings.Repeat(`{"key":"`+key+`"},`, n), ",") + `]}`
	}
	window := func(start, end string) string {
		return `{"sub":"s","issuer":"i","title":"T","windows":[{"start":"` + start + `","end":"` + end + `"}]}`
	}
	var keys []string
	for i := range MaxGuests + 1 {
		keys = append(keys, fmt.Sprintf(`{"key":"guestkey%08d"}`, i))
	}
	var windows []string
	for range MaxWindows + 1 {
		windows = append(windows, `{"start":"2026-11-14T13:00:00Z","end":"2026-11-14T14:00:00Z"}`)
	}
	for body, want := range map[string]error{
		`{"sub":"","issuer":"i","title":"T"}`:                                 ErrBadBody,
		`{"sub":"s","issuer":"","title":"T"}`:                                 ErrBadBody,
		`{"sub":"s","issuer":"i","title":"\u0000"}`:                           ErrBadBody,
		`{"sub":"s","issuer":"i","title":"` + strings.Repeat("x", 256) + `"}`: ErrBadBody,
		`{"sub":"s","issuer":"i","title":"T","member_subs":[""]}`:             ErrBadBody,
		`{"sub":"s","issuer":"i","title":"T","member_subs":` + many + `}`:     ErrBadBody,
		guests(1, "short"):               ErrBadBody,
		guests(1, "alice@example.com_x"): ErrBadBody,
		guests(2, "gkey_0123456789ab"):   ErrBadBody, // duplicate
		`{"sub":"s","issuer":"i","title":"T","guests":[` + strings.Join(keys, ",") + `]}`:     ErrBadBody,
		`{"sub":"s","issuer":"i","title":"T","guests":["gkey_0123456789ab"]}`:                 ErrBadBody,
		`{"sub":"s","issuer":"i","title":"T","windows":[` + strings.Join(windows, ",") + `]}`: ErrBadBody,
		window("2026-11-14T13:00:00+01:00", "2026-11-14T14:00:00Z"):                           ErrBadBody, // not UTC
		window("2026-11-14T14:00:00Z", "2026-11-14T13:00:00Z"):                                ErrBadBody, // ends before it starts
		window("2026-11-14T13:00:00Z", "2026-11-14T13:00:00Z"):                                ErrBadBody, // empty
		window("2027-02-14T13:00:00Z", "2027-02-14T14:00:00Z"):                                ErrBadBody, // past 91 days
		window("tomorrow", "2026-11-14T14:00:00Z"):                                            ErrBadBody,
		`{"sub":"s","issuer":"i","title":"T","windows":[{"start":null,"end":null}]}`:          ErrBadBody,
		`[]`: ErrBadBody,
	} {
		if _, err := DecodePut([]byte(body), now); !errors.Is(err, want) {
			t.Errorf("%.80s: %v, want %v", body, err, want)
		}
	}
}

func TestSameID(t *testing.T) {
	const id = "mtg_0123456789abcdef"
	for body, want := range map[string]bool{
		``:                               true,
		`{"sub":"s"}`:                    true,
		`{"id":"mtg_0123456789abcdef"}`:  true,
		`{"id":"mtg_another0123456789"}`: false,
		`{"id":5}`:                       false,
		`{"id":null}`:                    false,
		`{"id":"MTG_0123456789ABCDEF"}`:  false,
	} {
		if got := SameID([]byte(body), id); got != want {
			t.Errorf("%s: %v, want %v", body, got, want)
		}
	}
}

func TestVerify(t *testing.T) {
	secret := []byte(strings.Repeat("s", 32))
	replay := syncauth.NewMemoryReplay(syncauth.DefaultWindow, 100)
	send := func(signedAs, event string, body string) ([]byte, bool, error) {
		h, err := syncauth.Sign(secret, time.Now(), signedAs, "e1-"+event, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("PUT", "/api/meetings/x", strings.NewReader(body))
		h.Apply(r)
		b, _, replayed, err := Verify(r, secret, event, replay)
		return b, replayed, err
	}
	if b, replayed, err := send(EventPut, EventPut, `{}`); err != nil || replayed || string(b) != `{}` {
		t.Fatalf("good: %q %v %v", b, replayed, err)
	}
	if _, replayed, err := send(EventPut, EventPut, `{}`); err != nil || !replayed {
		t.Fatalf("replay: %v %v", replayed, err)
	}
	if _, _, err := send(EventClose, EventPut, `{}`); !errors.Is(err, syncauth.ErrBadSignature) {
		t.Fatalf("other type: %v", err)
	}
	if _, _, err := send(EventPut, EventPut, strings.Repeat(" ", MaxBody+1)); !errors.Is(err, syncauth.ErrBodyTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
}

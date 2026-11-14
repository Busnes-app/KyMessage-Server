package kycalendar

import (
	"errors"
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
	ok := `{"sub":"s","issuer":"https://id","title":"T","member_subs":["a"],"windows":[{"x":1}],"guests":[]}`
	if p, err := DecodePut([]byte(ok)); err != nil || p.Title != "T" || len(p.MemberSubs) != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	many := `["` + strings.Repeat(`a","`, MaxMembers) + `a"]`
	for body, want := range map[string]error{
		`{"sub":"s","issuer":"i","title":"T","guests":[{"key":"k"}]}`:         ErrGuests,
		`{"sub":"","issuer":"i","title":"T"}`:                                 ErrBadBody,
		`{"sub":"s","issuer":"","title":"T"}`:                                 ErrBadBody,
		`{"sub":"s","issuer":"i","title":"\u0000"}`:                           ErrBadBody,
		`{"sub":"s","issuer":"i","title":"` + strings.Repeat("x", 256) + `"}`: ErrBadBody,
		`{"sub":"s","issuer":"i","title":"T","member_subs":[""]}`:             ErrBadBody,
		`{"sub":"s","issuer":"i","title":"T","member_subs":` + many + `}`:     ErrBadBody,
		`[]`: ErrBadBody,
	} {
		if _, err := DecodePut([]byte(body)); !errors.Is(err, want) {
			t.Errorf("%.60s: %v, want %v", body, err, want)
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

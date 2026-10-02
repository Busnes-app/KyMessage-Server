package synapseadmin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// fakeSynapse refuses any token but "tok" as Synapse does an inactive one, then runs h.
func fakeSynapse(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"errcode":"M_UNKNOWN_TOKEN","error":"Token is not active"}`)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

func TestRoomsListsByNameWithSearch(t *testing.T) {
	c := fakeSynapse(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.URL.Path != "/_synapse/admin/v1/rooms" || q.Get("from") != "50" || q.Get("limit") != "25" ||
			q.Get("order_by") != "name" || q.Get("search_term") != "team chat" {
			http.Error(w, "bad request "+r.URL.String(), http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"rooms":[
			{"room_id":"!a:example.com","name":"Team chat","canonical_alias":"#team:example.com","joined_members":3,"joined_local_members":3,"version":"10","creator":"@alice:example.com","encryption":"m.megolm.v1.aes-sha2","federatable":true,"public":false,"join_rules":"invite","guest_access":null,"history_visibility":"shared","state_events":17,"room_type":null},
			{"room_id":"!b:example.com","name":null,"canonical_alias":null,"joined_members":1,"joined_local_members":1,"creator":"@bob:example.com","encryption":null,"public":true,"join_rules":"public","state_events":5}
		],"offset":50,"total_rooms":77}`)
	})
	got, err := c.Rooms(context.Background(), "tok", RoomQuery{Offset: 50, Limit: 25, Search: "team chat"})
	want := RoomPage{Total: 77, Rooms: []Room{
		{ID: "!a:example.com", Name: "Team chat", Alias: "#team:example.com", Creator: "@alice:example.com", Members: 3, LocalMembers: 3,
			Encryption: "m.megolm.v1.aes-sha2", JoinRule: "invite", StateEvents: 17},
		{ID: "!b:example.com", Creator: "@bob:example.com", Members: 1, LocalMembers: 1, Public: true, JoinRule: "public", StateEvents: 5},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v %v", got, err)
	}
}

// Synapse answers 400 to an empty search_term, so no search sends none.
func TestRoomsOmitsAnEmptySearch(t *testing.T) {
	c := fakeSynapse(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("search_term") {
			http.Error(w, `{"errcode":"M_INVALID_PARAM"}`, http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"rooms":[],"offset":0,"total_rooms":0}`)
	})
	if p, err := c.Rooms(context.Background(), "tok", RoomQuery{Limit: 1}); err != nil || p.Total != 0 {
		t.Fatalf("%+v %v", p, err)
	}
}

func TestErrorsNameTheCauseNeverTheToken(t *testing.T) {
	c := fakeSynapse(t, func(http.ResponseWriter, *http.Request) {})
	_, err := c.Rooms(context.Background(), "mpt_secret_value", RoomQuery{Limit: 1})
	var se *Error
	if !errors.As(err, &se) || se.Status != http.StatusUnauthorized || se.Errcode != "M_UNKNOWN_TOKEN" || se.Message != "Token is not active" {
		t.Fatalf("%v", err)
	}
	if strings.Contains(err.Error(), "mpt_secret_value") || !strings.Contains(err.Error(), "Token is not active") {
		t.Fatalf("error text %q", err.Error())
	}
}

func TestRefusesRedirectsAndForeignPaths(t *testing.T) {
	c := fakeSynapse(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://elsewhere.example/steal", http.StatusFound)
	})
	_, err := c.Rooms(context.Background(), "tok", RoomQuery{Limit: 1})
	var se *Error
	if !errors.As(err, &se) || se.Status != http.StatusFound {
		t.Fatalf("redirect followed or misreported: %v", err)
	}
	if err := c.do(context.Background(), "tok", http.MethodGet, "/_matrix/client/v3/account/whoami", nil, nil); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("foreign path: %v", err)
	}
}

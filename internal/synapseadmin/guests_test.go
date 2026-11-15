package synapseadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"testing"
)

func TestGuestHelpers(t *testing.T) {
	var sent []string
	joinRule, invite := "invite", "50"
	c := fakeSynapse(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			b, _ := io.ReadAll(r.Body)
			sent = append(sent, r.Method+" "+r.URL.EscapedPath()+" "+string(b))
			fmt.Fprint(w, `{}`)
			return
		}
		switch r.URL.Query().Get("type") {
		case "m.room.join_rules":
			fmt.Fprintf(w, `{"state":[{"type":"m.room.join_rules","state_key":"","content":{"join_rule":%q}}]}`, joinRule)
		case "m.room.power_levels":
			fmt.Fprintf(w, `{"state":[{"type":"m.room.power_levels","state_key":"","content":{"invite":%s,"kick":50,"users":{"@o:x":50}}}]}`, invite)
		case "m.room.member":
			fmt.Fprint(w, `{"state":[{"type":"m.room.member","sender":"@alice:x","state_key":"@guest-a:x","content":{"membership":"invite"}},
				{"type":"m.room.member","sender":"@guest-b:x","state_key":"@guest-b:x","content":{"membership":"knock"}}]}`)
		}
	})
	ctx := context.Background()
	if err := c.AllowKnocks(ctx, "tok", "!r:x"); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 || sent[0] != `PUT /_matrix/client/v3/rooms/%21r:x/state/m.room.join_rules/ {"join_rule":"knock"}` {
		t.Fatalf("sent %q", sent)
	}
	var levels map[string]any
	if err := json.Unmarshal([]byte(sent[1][len("PUT /_matrix/client/v3/rooms/%21r:x/state/m.room.power_levels/ "):]), &levels); err != nil ||
		levels["invite"] != 0.0 || levels["kick"] != 50.0 || levels["users"].(map[string]any)["@o:x"] != 50.0 {
		t.Fatalf("levels %q %v", sent[1], err)
	}
	sent, joinRule, invite = nil, "knock", "0"
	if err := c.AllowKnocks(ctx, "tok", "!r:x"); err != nil || len(sent) != 0 {
		t.Fatalf("already a waiting room: %q %v", sent, err)
	}
	if err := c.MarkGuest(ctx, "tok", "!r:x", "guest-a"); err != nil {
		t.Fatal(err)
	}
	if err := c.SetDisplayName(ctx, "tok", "@guest-a:x", "Bo (guest)"); err != nil {
		t.Fatal(err)
	}
	if err := c.Knock(ctx, "tok", "!r:x"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`PUT /_matrix/client/v3/rooms/%21r:x/state/app.kymessages.guest/guest-a {}`,
		`PUT /_matrix/client/v3/profile/@guest-a:x/displayname {"displayname":"Bo (guest)"}`,
		`POST /_matrix/client/v3/knock/%21r:x {}`,
	}
	if !slices.Equal(sent, want) {
		t.Fatalf("sent %q\nwant %q", sent, want)
	}
	m, err := c.MemberStates(ctx, "tok", "!r:x")
	if err != nil || m["@guest-a:x"] != (Member{"invite", "@alice:x"}) || m["@guest-b:x"].Membership != "knock" {
		t.Fatalf("members %v %v", m, err)
	}
	if err := c.do(ctx, "tok", http.MethodGet, clientPrefix+"user_directory/search", nil, nil); err == nil {
		t.Fatal("other client API paths stay refused")
	}
}

package api

// federationPreset is a public server an operator may tick at the curated level. The list is
// compiled in and changes only with a release; it is never fetched.
//
// Vetting criteria, each entry checked against all of them with its source:
//   - run by an identifiable organisation;
//   - federating for at least two years;
//   - a published abuse/moderation contact and policy;
//   - serves federation over valid TLS: the Matrix federation tester
//     (federationtester.matrix.org) reports FederationOK, valid certificates and an ed25519 key.
//     The console's own status probe (a remote profile lookup) needs a public, federating
//     Synapse, so it is each operator's "Check connections", not a vetting step.
//
// An entry nobody has verified against these is never listed.
type federationPreset struct {
	Name       string `json:"name"`
	Operator   string `json:"operator"`
	OpenSignup bool   `json:"open_signup"` // anyone can register there, so strangers can invite
	Source     string `json:"source"`      // where the criteria were checked
	Checked    string `json:"checked"`     // date checked, YYYY-MM-DD
}

// Checked against the criteria above: each server's /.well-known/matrix/server and its delegated
// /_matrix/federation/v1/version answered over valid TLS on 2026-10-11, and the federation
// tester reported FederationOK for all five on 2026-10-12. Four of the
// five are hosted by Element (modular.im / ems.host), so one Element incident can reach them
// together. Names, not hosts: delegation is resolved at use and has changed before. Failed and
// left out: opensuse.org (no reachable policy or contact), tchncs.de (run by an individual),
// envs.net (no Matrix server), gitter.im (no abuse contact), element.io (no published policy
// or support contact found).
var federationPresets = []federationPreset{
	// Terms from v1.0.0 (2018-08), Foundation incorporated 2018-10-29; abuse@matrix.org in its
	// MSC1929 support file; code of conduct; registration open at account.matrix.org.
	{Name: "matrix.org", Operator: "The Matrix.org Foundation C.I.C.", OpenSignup: true,
		Source: "https://matrix.org/legal/terms-and-conditions/", Checked: "2026-10-11"},
	// Since 2020-03-03 (matrix.org blog); matrix-admins@mozilla.com in its support file;
	// Community Participation Guidelines. Sign-up is through Mozilla's own sign-in, so open
	// sign-up is unverified and shown as open.
	{Name: "mozilla.org", Operator: "Mozilla", OpenSignup: true,
		Source: "https://www.mozilla.org/about/governance/policies/participation/", Checked: "2026-10-11"},
	// Since 2019-02-20 (dot.kde.org); Community Working Group community-wg@kde.org; code of
	// conduct; registrations closed (community.kde.org/Matrix).
	{Name: "kde.org", Operator: "KDE e.V.", OpenSignup: false,
		Source: "https://community.kde.org/Matrix", Checked: "2026-10-11"},
	// Public federation since 2021-06; incident report form; code of conduct naming Matrix;
	// registrations closed.
	{Name: "gnome.org", Operator: "GNOME Foundation", OpenSignup: false,
		Source: "https://conduct.gnome.org/", Checked: "2026-10-11"},
	// Since 2024-04-17 (Fedora Magazine; past two years at this check);
	// codeofconduct@fedoraproject.org; code of conduct; anyone with a free Fedora account.
	{Name: "fedora.im", Operator: "Fedora Project", OpenSignup: true,
		Source: "https://fedoramagazine.org/fedora-chat-your-gateway-to-matrix/", Checked: "2026-10-11"},
}

func presetByName(name string) *federationPreset {
	for i := range federationPresets {
		if federationPresets[i].Name == name {
			return &federationPresets[i]
		}
	}
	return nil
}

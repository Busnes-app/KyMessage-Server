package api

// federationPreset is a public server an operator may tick at the curated level. The list is
// compiled in and changes only with a release; it is never fetched.
//
// Vetting criteria, each entry checked against all of them with its source:
//   - run by an identifiable organisation;
//   - federating for at least two years;
//   - a published abuse/moderation contact and policy;
//   - serves federation over valid TLS and answers the federation status check.
//
// An entry nobody has verified against these is never listed.
type federationPreset struct {
	Name       string `json:"name"`
	Operator   string `json:"operator"`
	OpenSignup bool   `json:"open_signup"` // anyone can register there, so strangers can invite
	Source     string `json:"source"`      // where the criteria were checked
	Checked    string `json:"checked"`     // date checked, YYYY-MM-DD
}

var federationPresets = []federationPreset{}

func presetByName(name string) *federationPreset {
	for i := range federationPresets {
		if federationPresets[i].Name == name {
			return &federationPresets[i]
		}
	}
	return nil
}

package tool

// ActionAuthorization is operator-owned capability configuration, separate from
// business authority. Missing configuration grants no newly introduced action.
// Binding identity/generation is held by Binding; replacing it loses these grants.
type ActionAuthorization struct {
	Version  int           `json:"version"`
	Revision uint64        `json:"revision"`
	Grants   []ActionGrant `json:"grants"`
}
type ActionGrant struct {
	Operation  string `json:"operation"`
	TargetKind string `json:"target_kind"`
	ObjectType string `json:"object_type"`
}

func (a *ActionAuthorization) Valid() bool {
	if a == nil {
		return true
	}
	if a.Version != 1 || a.Revision == 0 || len(a.Grants) > 32 {
		return false
	}
	seen := map[ActionGrant]bool{}
	for _, g := range a.Grants {
		if g.Operation == "" || len(g.Operation) > 128 || (g.TargetKind != "object" && g.TargetKind != "message" && g.TargetKind != "discussion") || g.ObjectType == "" || len(g.ObjectType) > 128 || seen[g] {
			return false
		}
		seen[g] = true
	}
	return true
}

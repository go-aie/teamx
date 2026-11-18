package frozen

import (
	"encoding/json"
	"github.com/go-aie/teamx/internal/inputstream"
	"sort"
	"strconv"
)

type gitLabQueryCodec struct{ inputstream.Codec }

type gitLabQueryRow struct {
	ProjectID, Project, ObjectType, IID, Recipient, Channel, Text, Instance, Tenant string
}

func (c gitLabQueryCodec) Decode(schema int, b json.RawMessage) (Projection, error) {
	metadata, err := c.Codec.Decode(schema, b)
	p := Projection{Projection: metadata}
	if err != nil {
		return p, err
	}
	var row gitLabQueryRow
	if err := json.Unmarshal(b, &row); err != nil {
		return p, err
	}
	p.Text = row.Project + " " + row.Text
	if row.ProjectID != "" && row.Recipient != "" {
		p.Scopes = []Scope{{Key: inputstream.Ref("native_", p.Group.SourceKey("gitlab", row.Channel), row.ProjectID), Label: row.Project}}
	}
	return p, nil
}

func (gitLabQueryCodec) DescribeInput() Guidance {
	return Guidance{Version: "gitlab/filter/v1", Text: `Within this saved GitLab source, filter accepts project or project_id, object_type (merge_request or issue), iid and channel (events or todos). Use exact saved project names. iid requires a unique project; an ambiguous project returns choices. Filters select whole streams; read still delivers their context in order. Local saved evidence only; this help grants no remote permissions.`}
}

type gitLabInputIndex struct {
	rows map[inputstream.Location]gitLabQueryRow
}

func (gitLabQueryCodec) NewIndex() NativeIndex {
	return &gitLabInputIndex{rows: map[inputstream.Location]gitLabQueryRow{}}
}
func (i *gitLabInputIndex) Add(r inputstream.StoredRecord) error {
	var p gitLabQueryRow
	err := json.Unmarshal(r.Payload, &p)
	if err != nil {
		return err
	}
	if p.Channel != r.Channel {
		return inputstream.ErrIdentity
	}
	i.rows[r.Location] = p
	return nil
}
func (i *gitLabInputIndex) CompileInputFilter(c FilterContext, b json.RawMessage) (Selection, error) {
	var raw struct {
		Project    string      `json:"project"`
		ProjectID  json.Number `json:"project_id"`
		ObjectType string      `json:"object_type"`
		IID        json.Number `json:"iid"`
		Channel    string      `json:"channel"`
	}
	var f struct{ Project, ProjectID, ObjectType, IID, Channel string }
	if c.IndexVersion != 1 {
		return Selection{}, inputstream.ErrVersion
	}
	if err := DecodeFilter(b, &raw); err != nil {
		return Selection{}, err
	}
	f.Project = raw.Project
	f.ProjectID = raw.ProjectID.String()
	f.ObjectType = raw.ObjectType
	f.IID = raw.IID.String()
	f.Channel = raw.Channel
	for _, n := range []string{f.ProjectID, f.IID} {
		if n != "" && !inputPositiveNumber(n) {
			return Selection{}, inputstream.ErrInvalid
		}
	}
	if f.ObjectType != "" && f.ObjectType != "merge_request" && f.ObjectType != "issue" || f.Channel != "" && f.Channel != "events" && f.Channel != "todos" {
		return Selection{}, inputstream.ErrInvalid
	}
	projects := map[string]string{}
	named := map[string]string{}
	for _, l := range c.Allowed {
		p := i.rows[l]
		if p.ProjectID != "" {
			projects[p.ProjectID] = p.Project
			if f.Project == p.Project {
				named[p.ProjectID] = p.Project
			}
		}
	}
	if f.Project != "" && f.ProjectID != "" {
		if name, ok := projects[f.ProjectID]; ok && name != f.Project {
			return Selection{}, inputstream.ErrConflict
		}
	}
	if f.Project != "" && f.ProjectID == "" {
		if len(named) > 1 {
			return Selection{}, i.projectAmbiguity(named, c)
		}
		for id := range named {
			f.ProjectID = id
		}
	}
	if f.IID != "" && f.ProjectID == "" {
		if f.Project != "" && len(named) == 0 {
			return Selection{Missing: len(c.Allowed)}, nil
		}
		if len(projects) > 1 {
			return Selection{}, i.projectAmbiguity(projects, c)
		}
		for id := range projects {
			f.ProjectID = id
		}
		if f.ProjectID == "" {
			return Selection{Missing: len(c.Allowed)}, nil
		}
	}
	out := Selection{}
	for _, l := range c.Allowed {
		p, ok := i.rows[l]
		if !ok {
			return out, inputstream.ErrCorrupt
		}
		if (f.Project != "" || f.ProjectID != "" || f.IID != "") && (p.Recipient == "" || p.ProjectID == "") || f.IID != "" && p.IID == "" || f.ObjectType != "" && p.ObjectType == "" {
			out.Missing++
			continue
		}
		if f.Project != "" && p.Project != f.Project || f.ProjectID != "" && p.ProjectID != f.ProjectID || f.ObjectType != "" && p.ObjectType != f.ObjectType || f.IID != "" && p.IID != f.IID || f.Channel != "" && p.Channel != f.Channel {
			continue
		}
		out.Entries = append(out.Entries, l)
	}
	if f.ProjectID != "" {
		for _, l := range out.Entries {
			p := i.rows[l]
			g := inputstream.Group{Instance: p.Instance, Tenant: p.Tenant, Account: p.Recipient}
			scope := inputstream.Ref("native_", g.SourceKey("gitlab", p.Channel), f.ProjectID)
			if c.ScopeKey == g.SourceKey("gitlab", p.Channel) || c.ScopeKey == scope {
				out.ResolvedScope = scope
			}
			break
		}
	}
	return out, nil
}
func (i *gitLabInputIndex) projectAmbiguity(projects map[string]string, c FilterContext) error {
	choices := []Scope{}
	ids := []string{}
	for id := range projects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > 5 {
		ids = ids[:5]
	}
	if len(c.Allowed) > 0 {
		p := i.rows[c.Allowed[0]]
		g := inputstream.Group{Instance: p.Instance, Tenant: p.Tenant, Account: p.Recipient}
		for _, id := range ids {
			choices = append(choices, Scope{Key: inputstream.Ref("native_", g.SourceKey("gitlab", p.Channel), id), Label: projects[id] + " / " + id})
		}
	}

	return &ScopeError{Reason: ErrAmbiguous, Native: choices}
}

func (i *gitLabInputIndex) Clone() NativeIndex {
	out := &gitLabInputIndex{rows: make(map[inputstream.Location]gitLabQueryRow, len(i.rows))}
	for k, v := range i.rows {
		out.rows[k] = v
	}
	return out
}

func inputPositiveNumber(s string) bool {
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == s
}

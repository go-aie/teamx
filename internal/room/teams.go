package room

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Runtime is frozen with the Room; credential values are never read into it.
type teamRuntime struct {
	DSH struct {
		BinaryPath string       `json:"binary_path" toml:"binary_path"`
		BinaryArgs []string     `json:"binary_args,omitempty" toml:"binary_args"`
		Artifact   dsh.Artifact `json:"artifact,omitzero" toml:"artifact"`
	} `json:"dsh" toml:"dsh"`
	Model dsh.ModelConfig `json:"model" toml:"model"`
}

type teamMember struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Instructions     string            `json:"instructions,omitempty"`
	InstructionsFile string            `json:"instructions_file,omitempty"`
	ToolEnv          map[string]string `json:"tool_env,omitempty"`
}

type teamDefinition struct {
	ID             string           `json:"id"`
	Name           string           `json:"name"`
	Description    string           `json:"description"`
	OpeningMessage string           `json:"opening_message"`
	Runtime        *teamRuntime     `json:"runtime,omitempty"`
	Members        []teamMember     `json:"members"`
	Workspace      *workspaceConfig `json:"workspace,omitempty"`
	resources      []roomResource
}

type teamSummary struct {
	ID             string              `json:"id"`
	Name           string              `json:"name"`
	Description    string              `json:"description"`
	OpeningMessage string              `json:"opening_message"`
	Members        []teamMemberSummary `json:"members"`
}

type teamMemberSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (r teamRuntime) profile() dshProfile {
	return dshProfile{BinaryPath: r.DSH.BinaryPath, BinaryArgs: slices.Clone(r.DSH.BinaryArgs), Artifact: r.DSH.Artifact, Model: r.Model.Clone()}
}

func validateTeamRuntime(r teamRuntime) error {
	p := r.profile()
	return dsh.CheckConfig(dsh.Options{BinaryPath: p.BinaryPath, BinaryArgs: p.BinaryArgs, Artifact: p.Artifact, Model: p.Model})
}

// A missing directory is an empty catalog. Runtime overrides replace the whole default.
func loadTeams(dir string, defaults *teamRuntime) (map[string]teamDefinition, error) {
	teams := map[string]teamDefinition{}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return teams, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		root := filepath.Join(dir, entry.Name())
		source := os.DirFS(root)
		var team teamDefinition
		if err := readTeamFile(source, "team.json", &team); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("%s/team.json: %w", root, err)
		}
		if team.Runtime == nil && defaults != nil {
			resolved := *defaults
			resolved.DSH.BinaryArgs = slices.Clone(defaults.DSH.BinaryArgs)
			resolved.Model = defaults.Model.Clone()
			team.Runtime = &resolved
		}
		resources, err := collectResources(source, ".", map[string]bool{"team.json": true})
		if err != nil {
			return nil, fmt.Errorf("team %s resources: %w", team.ID, err)
		}
		if err := validateTeam(&team, source); err != nil {
			return nil, fmt.Errorf("%s/team.json: %w", root, err)
		}
		if _, exists := teams[team.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate team id %q in %s", ErrRequest, team.ID, root)
		}
		if len(teams) >= maxMembers {
			return nil, ErrCapacity
		}
		team.resources = resources
		teams[team.ID] = team
	}
	return teams, nil
}

func validateTeam(team *teamDefinition, source fs.FS) error {
	if !teamIdentifier(team.ID) || !validText(team.Name) || len(team.Name) > 256 || !validText(team.Description) ||
		!validText(team.OpeningMessage) || len(team.OpeningMessage) > maxTextBytes || len(team.Members) == 0 || len(team.Members) > maxMembers {
		return fmt.Errorf("%w: id, name, description, opening_message and 1..%d members required", ErrRequest, maxMembers)
	}
	if team.Runtime == nil {
		return fmt.Errorf("%w: runtime required in config.toml or team.json", ErrRequest)
	}
	if err := validateTeamRuntime(*team.Runtime); err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	if team.Workspace != nil {
		w, err := normalizeWorkspace(*team.Workspace, true)
		if err != nil {
			return err
		}
		team.Workspace = &w
	}
	seen := map[string]bool{}
	for i := range team.Members {
		member := &team.Members[i]
		if !teamIdentifier(member.ID) || !validText(member.Name) || seen[member.ID] {
			return fmt.Errorf("%w: invalid or duplicate member id %q", ErrRequest, member.ID)
		}
		seen[member.ID] = true
		if (member.Instructions == "") == (member.InstructionsFile == "") {
			return fmt.Errorf("%w: member %s requires exactly one of instructions and instructions_file", ErrRequest, member.ID)
		}
		if member.InstructionsFile != "" {
			if !safeResourcePath(member.InstructionsFile) {
				return fmt.Errorf("%w: member %s instructions_file %q", ErrRequest, member.ID, member.InstructionsFile)
			}
			// collectResources rejects all symlinks, including parent components.
			data, err := fs.ReadFile(source, member.InstructionsFile)
			if err != nil {
				return fmt.Errorf("member %s instructions_file: %w", member.ID, err)
			}
			member.Instructions = string(data)
		}
		if !validText(member.Instructions) || len(member.Instructions) > 1<<20 {
			return fmt.Errorf("%w: member %s instructions", ErrRequest, member.ID)
		}
		if err := dsh.CheckToolEnv(member.ToolEnv, team.Runtime.Model.APIKeyEnv); err != nil {
			return fmt.Errorf("member %s: %w", member.ID, err)
		}
		for k, v := range member.ToolEnv {
			if isRoomToolEnv(k) || k == "TEAMX_CLI" {
				return fmt.Errorf("%w: member %s overrides identity %s", ErrIdentity, member.ID, k)
			}
			// Explicit relative resource bindings are resolved inside each Room copy.
			if strings.HasPrefix(v, "./") && !safeResourcePath(strings.TrimPrefix(v, "./")) {
				return fmt.Errorf("%w: member %s tool_env %s resource path", ErrRequest, member.ID, k)
			}
			if strings.HasPrefix(v, "./") {
				if _, err := fs.Stat(source, strings.TrimPrefix(v, "./")); err != nil {
					return fmt.Errorf("member %s tool_env %s: %w", member.ID, k, err)
				}
			}
		}
	}
	return nil
}

func safeResourcePath(p string) bool {
	return fs.ValidPath(p) && p != "." && !strings.Contains(p, "\\")
}

func collectResources(source fs.FS, root string, excluded map[string]bool) ([]roomResource, error) {
	resources := []roomResource{}
	total := 0
	err := fs.WalkDir(source, root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: symlink resource %s", ErrRequest, p)
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") && p != root || strings.HasPrefix(entry.Name(), "_") {
				return fs.SkipDir
			}
			return nil
		}
		rel := strings.TrimPrefix(p, root+"/")
		if root == "." {
			rel = p
		}
		if excluded[rel] {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 4<<20 || int64(total)+info.Size() > 4<<20 || len(resources) >= 1024 {
			return ErrCapacity
		}
		data, err := fs.ReadFile(source, p)
		if err != nil {
			return err
		}
		total += len(data)
		if total > 4<<20 {
			return ErrCapacity
		}
		resources = append(resources, roomResource{Path: rel, Data: data, Executable: strings.HasSuffix(rel, ".sh") || info.Mode()&0111 != 0})
		return nil
	})
	return resources, err
}

func (m *roomManager) prepare(selection roomSelection) (roomIndexEntry, error) {
	team, ok := m.teams[selection.TeamID]
	if !ok {
		return roomIndexEntry{}, fmt.Errorf("%w: team %q unavailable; configure --teams-dir", ErrRequest, selection.TeamID)
	}
	// Reuse identity allocation and durable initialization, with no runtime lookup.
	id, err := newOpaqueID()
	if err != nil {
		return roomIndexEntry{}, err
	}
	instance, err := newOpaqueID()
	if err != nil {
		return roomIndexEntry{}, err
	}
	name := selection.Name
	if name == "" {
		name = team.Name
	}
	e := newTeamEntry(selection, id, instance, name)
	w := workspaceConfig{Root: "workspaces"}
	if team.Workspace != nil {
		w.Root = team.Workspace.Root // Repository/binding are suggestions only.
	}
	runtime := *team.Runtime
	runtime.DSH.BinaryArgs = slices.Clone(team.Runtime.DSH.BinaryArgs)
	runtime.Model = team.Runtime.Model.Clone()
	init := &roomInitialization{Config: managedRoomConfig{PlatformToolsVersion: 1, Version: managedConfigVersion, RoomIdentity: e.RoomIdentity, Name: name, TeamID: team.ID,
		Runtime: &runtime, OpeningMessage: team.OpeningMessage, SessionMaxTurns: m.maxTurns, Workspace: &w, WorkspaceVersion: 1}, Resources: team.resources}
	for _, member := range team.Members {
		membership, err := newOpaqueID()
		if err != nil {
			return e, err
		}
		init.Config.Members = append(init.Config.Members, managedMember{MemberState: MemberState{AgentID: member.ID, MembershipID: membership,
			Runner: RunnerConfig{Kind: "dsh", Profile: "team/" + team.ID + "/" + member.ID}}, Name: member.Name, Instructions: member.Instructions,
			ToolEnv: member.ToolEnv, Workspace: workspaceMemberPath(w, membership)})
	}
	e.Initialization = init
	return e, validateInitialization(e, *init)
}

func newTeamEntry(selection roomSelection, id, instance, name string) roomIndexEntry {
	now := time.Now().UTC()
	return roomIndexEntry{roomSummary: roomSummary{RoomIdentity: RoomIdentity{RoomID: id, RoomInstance: instance}, TeamID: selection.TeamID, Name: name, CreateID: selection.CreateID, Status: "creating", CreatedAt: now, UpdatedAt: now}, Selection: selection}
}

func validSelection(s roomSelection) bool {
	return teamIdentifier(s.TeamID)
}
func validRoomSource(e roomIndexEntry) bool {
	return teamIdentifier(e.TeamID)
}
func managedRunner(c managedRoomConfig, agent string) RunnerConfig {
	return RunnerConfig{Kind: "dsh", Profile: "team/" + c.TeamID + "/" + agent}
}
func newTeamRoomManager(dir, teamsDir string, maxTurns uint64, defaults *teamRuntime) (*roomManager, error) {
	abs, err := filepath.Abs(teamsDir)
	if err != nil {
		return nil, err
	}
	teams, err := loadTeams(abs, defaults)
	if err != nil {
		return nil, err
	}
	m, err := newRoomManager(dir, maxTurns)
	if err != nil {
		return nil, err
	}
	m.teams, m.teamsDir = teams, abs
	return m, nil
}
func (m *roomManager) teamSummaries() []teamSummary {
	result := []teamSummary{}
	for _, t := range m.teams {
		summary := teamSummary{ID: t.ID, Name: t.Name, Description: t.Description, OpeningMessage: t.OpeningMessage}
		for _, member := range t.Members {
			summary.Members = append(summary.Members, teamMemberSummary{ID: member.ID, Name: member.Name})
		}
		result = append(result, summary)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func resolveTeamResource(dir, value string) string {
	if strings.HasPrefix(value, "./") {
		return filepath.Join(dir, "resources", filepath.FromSlash(strings.TrimPrefix(value, "./")))
	}
	return value
}

func teamIdentifier(value string) bool {
	return len(value) > 0 && len(value) <= 64 && strings.Trim(value, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") == ""
}

func readTeamFile(source fs.FS, name string, out any) error {
	f, err := source.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return ErrCapacity
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	if dec.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}

package room

import (
	"fmt"
	"sync"
)

// These are deployment admission limits, shared by every Space and Room.
// They count owned Sessions (including idle and incompletely closed processes),
// rather than only active prompts. No queue or automatic eviction is introduced.
type executionResourceLimits struct {
	Spaces              int `toml:"spaces"`
	ActiveSpaces        int `toml:"active_spaces"`
	SpacesPerUser       int `toml:"spaces_per_user"`
	ActiveSpacesPerUser int `toml:"active_spaces_per_user"`
	Sessions            int `toml:"sessions"`
	SessionsPerUser     int `toml:"sessions_per_user"`
	SessionsPerSpace    int `toml:"sessions_per_space"`
}

func (l executionResourceLimits) validate() error {
	for _, v := range []int{l.Spaces, l.ActiveSpaces, l.SpacesPerUser, l.ActiveSpacesPerUser, l.Sessions, l.SessionsPerUser, l.SessionsPerSpace} {
		if v < 0 || v > 65536 {
			return fmt.Errorf("deployment.resources limits must be between 0 and 65536")
		}
	}
	return nil
}

type executionResources struct {
	mu       sync.Mutex
	limits   executionResourceLimits
	sessions int
	users    map[string]int
	spaces   map[string]int
}

func newExecutionResources(l executionResourceLimits) *executionResources {
	return &executionResources{limits: l, users: map[string]int{}, spaces: map[string]int{}}
}

func withinResourceLimit(limit, count int) bool { return limit == 0 || count <= limit }

func (r *executionResources) acquireSession(user, space string) (func(), error) {
	if r == nil {
		return func() {}, nil
	}
	if user == "" || space == "" {
		return nil, ErrIdentity
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !withinResourceLimit(r.limits.Sessions, r.sessions+1) ||
		!withinResourceLimit(r.limits.SessionsPerUser, r.users[user]+1) ||
		!withinResourceLimit(r.limits.SessionsPerSpace, r.spaces[space]+1) {
		return nil, fmt.Errorf("%w: deployment Session capacity exhausted; close an idle Session or increase administrator limits", ErrBusy)
	}
	r.sessions++
	r.users[user]++
	r.spaces[space]++
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.sessions--
			r.users[user]--
			r.spaces[space]--
			if r.users[user] == 0 {
				delete(r.users, user)
			}
			if r.spaces[space] == 0 {
				delete(r.spaces, space)
			}
		})
	}, nil
}

// The caller holds the directory lock. Archiving reservations remain active
// until the original lifecycle owner has durably committed suspended.
func (r *executionResources) checkSpaces(records []SpaceRecord) error {
	if r == nil {
		return nil
	}
	active := 0
	users, activeUsers := map[string]int{}, map[string]int{}
	for _, record := range records {
		users[record.OwnerUserID]++
		if record.Status != "suspended" {
			active++
			activeUsers[record.OwnerUserID]++
		}
	}
	for user, count := range users {
		if !withinResourceLimit(r.limits.SpacesPerUser, count) || !withinResourceLimit(r.limits.ActiveSpacesPerUser, activeUsers[user]) {
			return fmt.Errorf("%w: user Space capacity exhausted", ErrBusy)
		}
	}
	if !withinResourceLimit(r.limits.Spaces, len(records)) || !withinResourceLimit(r.limits.ActiveSpaces, active) {
		return fmt.Errorf("%w: deployment Space capacity exhausted", ErrBusy)
	}
	return nil
}

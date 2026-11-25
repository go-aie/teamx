package room

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

type spacePathClaim struct {
	SpaceID string `json:"space_id"`
	RoomIdentity
	Paths []string `json:"paths"`
}
type spaceClaimSnapshot struct {
	Computers map[string]string `json:"computers,omitempty"`
	Version   int               `json:"version"`
	Claims    []spacePathClaim  `json:"claims"`
}

// One durable admission fence across managers. Never acquires a manager lock;
// the owning Room manager still persists and settles the workspace operation.
type spaceDirectoryClaims struct {
	mu        sync.Mutex
	home      string
	directory spaceDirectory
	writer    *snapshotWriter
	saved     spaceClaimSnapshot
}

func (c *spaceDirectoryClaims) restore() error {
	c.saved.Version = 1
	if err := readIdentitySnapshot(filepath.Join(c.home, "workspace-claims.json"), &c.saved); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if c.saved.Version != 1 {
		return ErrInvalid
	}
	for _, claim := range c.saved.Claims {
		found := false
		for _, record := range c.directory.Spaces {
			if claim.SpaceID == record.ID {
				found = true
			}
		}
		if !found {
			return ErrIdentity
		}
		for _, path := range claim.Paths {
			real, err := resolveFutureDirectory(path)
			if err != nil || path != real {
				return ErrIdentity
			}
		}
	}
	// Adopt existing external directories before any manager starts recovery.
	for _, record := range c.directory.Spaces {
		if record.resetting() {
			continue
		} // retain durable claims; do not scan partially cleaned indexes
		startup, err := readStartupConfig(filepath.Join(record.Roots.Home, "config.toml"), true, false)
		if err == nil && startup.Computer.InstanceID != "" {
			if err := c.reserveComputer(record.ID, startup.Computer.InstanceID); err != nil {
				return err
			}
		}
		var journal computerJournal
		if readSnapshot(filepath.Join(record.Roots.Home, "computer-state", "connection.json"), &journal, 4<<20) == nil {
			for _, op := range journal.Operations {
				if op.Target.InstanceID != "" {
					if err := c.reserveComputer(record.ID, op.Target.InstanceID); err != nil {
						return err
					}
				}
			}
		}
		var index roomsIndex
		if err := readSnapshot(filepath.Join(record.Roots.Data, "rooms.json"), &index, maxRoomFileBytes); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			continue
		} // a failed Space cannot block healthy Spaces
		for _, room := range index.Rooms {
			var config managedRoomConfig
			if err := readSnapshot(filepath.Join(record.Roots.Data, "rooms", room.RoomID, "room-config.json"), &config, maxRoomFileBytes); err != nil {
				continue
			}
			if config.Workspace != nil {
				if err := c.reserve(record.ID, room.RoomIdentity, config.Workspace.MemberDirectories); err != nil {
					return err
				}
			}
		}
		for _, op := range index.WorkspaceOperations {
			if !op.finished() {
				if err := c.reserve(record.ID, op.RoomIdentity, op.Target.MemberDirectories); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
func (c *spaceDirectoryClaims) checkLocked(space string, paths map[string]string) error {
	for _, path := range paths {
		for _, record := range c.directory.Spaces {
			if record.ID == space {
				continue
			}
			for _, root := range []string{record.Roots.Home, record.Roots.Data, record.Roots.Teams} {
				if absoluteDirectoriesOverlap(path, root) {
					return directoryError("directory_conflict")
				}
			}
		}
		for _, claim := range c.saved.Claims {
			if claim.SpaceID == space {
				continue
			}
			for _, other := range claim.Paths {
				if absoluteDirectoriesOverlap(path, other) {
					return directoryError("directory_conflict")
				}
			}
		}
	}
	return nil
}
func (c *spaceDirectoryClaims) check(space string, paths map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.checkLocked(space, paths)
}
func (c *spaceDirectoryClaims) reserve(space string, room RoomIdentity, paths map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkLocked(space, paths); err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	next := spaceClaimSnapshot{Version: 1, Computers: c.saved.Computers, Claims: append([]spacePathClaim{}, c.saved.Claims...)}
	index := -1
	for i, claim := range next.Claims {
		if claim.SpaceID == space && claim.RoomIdentity == room {
			index = i
			break
		}
	}
	if index < 0 {
		next.Claims = append(next.Claims, spacePathClaim{SpaceID: space, RoomIdentity: room})
		index = len(next.Claims) - 1
	}
	claim := next.Claims[index]
	claim.Paths = append([]string{}, claim.Paths...)
	for _, path := range paths {
		if !slices.Contains(claim.Paths, path) {
			claim.Paths = append(claim.Paths, path)
		}
	}
	if index < len(c.saved.Claims) && slices.Equal(claim.Paths, c.saved.Claims[index].Paths) {
		return nil
	}
	next.Claims[index] = claim
	if err := c.writer.save(filepath.Join(c.home, "workspace-claims.json"), next, maxPrivateFileBytes); err != nil {
		return err
	}
	c.saved = next
	return nil
}

func (r *SpaceRuntime) bindComputerClaims(control *computerController) error {
	if r.config.directoryClaims == nil {
		return nil
	}
	control.claimInstance = func(instance string) error { return r.config.directoryClaims.reserveComputer(r.SpaceID, instance) }
	if instance := control.current.Load().InstanceID; instance != "" {
		if err := control.claimInstance(instance); err != nil {
			return err
		}
	}
	for _, op := range control.journal.Operations {
		if op.Status != "completed" && op.Target.InstanceID != "" {
			if err := control.claimInstance(op.Target.InstanceID); err != nil {
				return err
			}
		}
	}
	return nil
}
func (c *spaceDirectoryClaims) reserveComputer(space, instance string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if instance == "" {
		return ErrIdentity
	}
	if owner := c.saved.Computers[instance]; owner != "" {
		if owner == space {
			return nil
		}
		return ErrConflict
	}
	next := c.saved
	next.Computers = map[string]string{}
	for key, value := range c.saved.Computers {
		next.Computers[key] = value
	}
	next.Computers[instance] = space
	if err := c.writer.save(filepath.Join(c.home, "workspace-claims.json"), next, maxPrivateFileBytes); err != nil {
		return err
	}
	c.saved = next
	return nil
}

// Release old external paths only after the owning operation is durably settled.
// Failures retain the conservative reservation and block writes until repaired.
func (c *spaceDirectoryClaims) settled(space string, room RoomIdentity, paths map[string]string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.checkLocked(space, paths); err != nil {
		return err
	}
	next := c.saved
	next.Claims = append([]spacePathClaim{}, c.saved.Claims...)
	for i, claim := range next.Claims {
		if claim.SpaceID != space || claim.RoomIdentity != room {
			continue
		}
		claim.Paths = nil
		for _, path := range paths {
			if !slices.Contains(claim.Paths, path) {
				claim.Paths = append(claim.Paths, path)
			}
		}
		slices.Sort(claim.Paths)
		next.Claims[i] = claim
		if err := c.writer.save(filepath.Join(c.home, "workspace-claims.json"), next, maxPrivateFileBytes); err != nil {
			return err
		}
		c.saved = next
		return nil
	}
	return nil
}

func (c *spaceDirectoryClaims) checkNewSpaceLocked(record SpaceRecord) error {
	for _, claim := range c.saved.Claims {
		for _, path := range claim.Paths {
			for _, root := range []string{record.Roots.Home, record.Roots.Data, record.Roots.Teams} {
				if absoluteDirectoriesOverlap(path, root) {
					return directoryError("directory_conflict")
				}
			}
		}
	}
	return nil
}

// Reset releases only business directory bindings after all old writers settle.
// Computer instance reservations remain owned by this same Space.
func (c *spaceDirectoryClaims) releaseResetSpace(space string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	next := c.saved
	next.Claims = nil
	for _, claim := range c.saved.Claims {
		if claim.SpaceID != space {
			next.Claims = append(next.Claims, claim)
		}
	}
	if len(next.Claims) == len(c.saved.Claims) {
		return nil
	}
	if err := c.writer.save(filepath.Join(c.home, "workspace-claims.json"), next, maxPrivateFileBytes); err != nil {
		return err
	}
	c.saved = next
	return nil
}

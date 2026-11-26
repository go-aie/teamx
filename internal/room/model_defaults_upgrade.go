package room

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

const hostedDefaultUpgradePrefix = "hosted-default-advanced-v1-"

// Upgrade the original automatically provisioned entry before restoring Room
// workers. Validation, publication, settlement and generation changes remain
// owned by the normal model controller. Completed upgrades never overwrite
// subsequent user edits; an interrupted application continues its exact target.
func (c *modelController) upgradeHostedDefaultModel(owner string) error {
	c.manager.mu.Lock()
	var previous *modelChangeOperation
	for _, op := range c.operations {
		if strings.HasPrefix(op.ID, hostedDefaultUpgradePrefix) && op.Owner == owner && (previous == nil || op.CreatedAt.After(previous.CreatedAt)) {
			previous = op
		}
	}
	if previous != nil {
		if previous.Status == "completed" || previous.Status == "cancelled" {
			c.manager.mu.Unlock()
			return nil
		}
		if previous.Target != nil {
			if !c.running[previous.ID] {
				c.launchLocked(previous, true)
			}
			c.manager.mu.Unlock()
			return c.waitHostedDefaultUpgrade(previous.ID)
		}
	}
	c.manager.mu.Unlock()
	catalog := c.service.snapshot()
	// Only the uncustomized entry produced by the old bootstrap is eligible.
	// Other routes, entries, defaults, credentials and per-member choices stay
	// under their existing owners.
	legacy := ModelParameters{Protocol: "openai-completions", BaseURL: hostedGatewayBaseURL, ModelID: hostedDefaultModel}
	for _, entry := range catalog.Entries {
		if entry.ID != "daily" || entry.PendingDeletion || !reflect.DeepEqual(entry.ModelParameters, legacy) {
			continue
		}
		entry.ModelParameters = hostedDefaultModelParameters()
		id, err := newOpaqueID()
		if err != nil {
			return err
		}
		id = hostedDefaultUpgradePrefix + id
		if _, err := c.submit(modelChangeInput{RequestID: id, ExpectedVersion: catalog.Version, Kind: "save", Entry: entry}, owner); err != nil {
			return err
		}
		return c.waitHostedDefaultUpgrade(id)
	}
	return nil
}

func (c *modelController) waitHostedDefaultUpgrade(id string) error {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		c.manager.mu.Lock()
		op := c.operations[id]
		running, status, message := c.running[id], op.Status, op.Message
		c.manager.mu.Unlock()
		if !running {
			if status != "completed" {
				return fmt.Errorf("default model upgrade %s: %s", id, message)
			}
			return nil
		}
		select {
		case <-c.ctx.Done():
			return c.ctx.Err()
		case <-ticker.C:
		}
	}
}

// Package agents coordinates member execution. The turn scheduler owns grants;
// member readiness, backend execution and recovery are supplied by its callers.
package agents

import (
	"errors"
	"fmt"
)

// MaxMembers bounds the scheduler's member incarnations and the Room roster.
const MaxMembers = 32

var ErrCapacity = errors.New("room capacity exceeded")

// SchedulingMode is frozen with the Room, independently of Session/model identity.
type SchedulingMode string

const (
	SchedulingSerial   SchedulingMode = "serial"
	SchedulingParallel SchedulingMode = "parallel"
)

func (m SchedulingMode) Effective() (SchedulingMode, error) {
	switch m {
	case "", SchedulingSerial:
		return SchedulingSerial, nil
	case SchedulingParallel:
		return SchedulingParallel, nil
	default:
		return "", fmt.Errorf("invalid scheduling mode %q", m)
	}
}

package room

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/go-aie/teamx/internal/app/agents"
)

// schedulingMode is frozen with the Room, independently of Session/model identity.
// This configuration adapter preserves the existing JSON and ErrRequest contract.
// Template/configuration callers remain in Room until their R7/R8 migration.
type schedulingMode agents.SchedulingMode

const (
	schedulingSerial   schedulingMode = schedulingMode(agents.SchedulingSerial)
	schedulingParallel schedulingMode = schedulingMode(agents.SchedulingParallel)
)

func (m schedulingMode) effective() (schedulingMode, error) {
	mode, err := agents.SchedulingMode(m).Effective()
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRequest, err)
	}
	return schedulingMode(mode), nil
}

func (m *schedulingMode) UnmarshalJSON(data []byte) error {
	// JSON null is not an omitted string setting.
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("%w: scheduling must be a string", ErrRequest)
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	mode := schedulingMode(value)
	if _, err := mode.effective(); err != nil {
		return err
	}
	// Preserve absence/empty values so legacy receipts remain byte-equivalent.
	*m = mode
	return nil
}

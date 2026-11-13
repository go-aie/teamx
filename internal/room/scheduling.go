package room

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// schedulingMode is frozen with the Room, independently of Session/model identity.
type schedulingMode string

const (
	schedulingSerial   schedulingMode = "serial"
	schedulingParallel schedulingMode = "parallel"
)

func (m schedulingMode) effective() (schedulingMode, error) {
	switch m {
	case "", schedulingSerial:
		return schedulingSerial, nil
	case schedulingParallel:
		return schedulingParallel, nil
	default:
		return "", fmt.Errorf("%w: invalid scheduling mode %q", ErrRequest, m)
	}
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

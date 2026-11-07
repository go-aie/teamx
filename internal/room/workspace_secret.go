package room

import (
	"encoding/json"
	"fmt"
	"io"
)

// Opaque short-lived material used only at service execution boundaries.
type workspaceSecret struct{ value string }

func (workspaceSecret) MarshalJSON() ([]byte, error) { return json.Marshal("[REDACTED]") }
func (workspaceSecret) Format(s fmt.State, _ rune)   { _, _ = io.WriteString(s, "[REDACTED]") }

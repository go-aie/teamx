package calendar

import (
	"bytes"
	"encoding/json"
)

// ApplyChanges replaces each supplied business field as a whole. In particular,
// scope and schedule never inherit omitted nested keys from the previous value.
func ApplyChanges(d Definition, changes map[string]json.RawMessage, zone string) (Definition, error) {
	for key, raw := range changes {
		null := bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
		var target any
		switch key {
		case "title":
			target = &d.Title
		case "instruction":
			target = &d.Instruction
		case "scope":
			d.Scope = nil
			if !null {
				d.Scope = append(json.RawMessage(nil), raw...)
			}
			continue
		case "schedule":
			d.Schedule = Schedule{}
			target = &d.Schedule
		case "misfire":
			if null {
				d.Misfire = "latest_within_one_hour"
				continue
			}
			target = &d.Misfire
		default:
			return Definition{}, Fail("invalid_request", "changes."+key, "field is not editable")
		}
		if null {
			return Definition{}, Fail("invalid_request", "changes."+key, "required field cannot be cleared")
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(target); err != nil {
			return Definition{}, Fail("invalid_request", "changes."+key, "invalid replacement")
		}
	}
	if d.Schedule.TimeZone == "" {
		d.Schedule.TimeZone = zone
	}
	if d.Schedule.DayFilter == "" {
		d.Schedule.DayFilter = "all_days"
	}
	if d.Misfire == "" {
		d.Misfire = "latest_within_one_hour"
	}
	return d, nil
}

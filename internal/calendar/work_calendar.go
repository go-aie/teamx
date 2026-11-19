package calendar

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"time"
)

//go:embed data/*.json
var workCalendarFiles embed.FS

// WorkYear is an offline, reviewed official annual calendar. Uncovered dates
// are unknown, including ordinary weekdays; they never inherit another year.
type WorkYear struct {
	Region    string            `json:"region"`
	From      string            `json:"from"`
	To        string            `json:"to"`
	Version   string            `json:"version"`
	Source    string            `json:"source"`
	Published string            `json:"published"`
	Days      map[string]string `json:"days"`
}
type WorkCalendars struct{ years []WorkYear }
type WorkDay struct {
	Date    string `json:"date"`
	Kind    string `json:"kind"`
	Version string `json:"version,omitempty"`
}
type WorkCoverage struct {
	Region    string `json:"region"`
	From      string `json:"from"`
	To        string `json:"to"`
	Version   string `json:"version"`
	Source    string `json:"source"`
	Published string `json:"published"`
}

func ParseWorkCalendars(files ...[]byte) (*WorkCalendars, error) {
	b := &WorkCalendars{}
	for _, raw := range files {
		var y WorkYear
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&y); err != nil {
			return nil, err
		}
		// Decode through a token pass as encoding/json otherwise accepts duplicate
		// day keys silently (including contradictory holiday/makeup overrides).
		if err := rejectDuplicateJSON(raw); err != nil {
			return nil, err
		}
		from, e1 := dateValue(y.From)
		to, e2 := dateValue(y.To)
		_, e3 := dateValue(y.Published)
		u, e4 := url.Parse(y.Source)
		if y.Region != "CN" || e1 != nil || e2 != nil || from.Month() != 1 || from.Day() != 1 || to.Year() != from.Year() || to.Month() != 12 || to.Day() != 31 || y.Version == "" || e3 != nil || e4 != nil || u.Scheme != "https" || u.Host == "" || y.Days == nil {
			return nil, fmt.Errorf("invalid annual work calendar %q", y.Version)
		}
		for d, k := range y.Days {
			if _, err := dateValue(d); err != nil || d < y.From || d > y.To || k != "holiday" && k != "makeup" {
				return nil, fmt.Errorf("invalid work calendar date %q", d)
			}
		}
		for _, prev := range b.years {
			if y.From <= prev.To && y.To >= prev.From {
				return nil, fmt.Errorf("overlapping work calendar coverage")
			}
		}
		b.years = append(b.years, y)
	}
	sort.Slice(b.years, func(i, j int) bool { return b.years[i].From < b.years[j].From })
	return b, nil
}
func rejectDuplicateJSON(raw []byte) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if tok == json.Delim('{') {
			seen := map[string]bool{}
			for dec.More() {
				k, err := dec.Token()
				if err != nil {
					return err
				}
				key := k.(string)
				if seen[key] {
					return fmt.Errorf("duplicate work calendar key %s", key)
				}
				seen[key] = true
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		if tok == json.Delim('[') {
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing work calendar JSON: %v", err)
	}
	return nil
}
func LoadWorkCalendars() (*WorkCalendars, error) {
	entries, err := workCalendarFiles.ReadDir("data")
	if err != nil {
		return nil, err
	}
	files := [][]byte{}
	for _, e := range entries {
		raw, err := workCalendarFiles.ReadFile("data/" + e.Name())
		if err != nil {
			return nil, err
		}
		files = append(files, raw)
	}
	return ParseWorkCalendars(files...)
}
func (b *WorkCalendars) Day(date string) WorkDay {
	v := WorkDay{Date: date, Kind: "unknown"}
	if b == nil {
		return v
	}
	for _, y := range b.years {
		if date < y.From || date > y.To {
			continue
		}
		v.Version = y.Version
		v.Kind = y.Days[date]
		if v.Kind == "" {
			d, err := dateValue(date)
			if err != nil {
				return WorkDay{Date: date, Kind: "unknown"}
			}
			v.Kind = "workday"
			if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
				v.Kind = "weekend"
			}
		}
		return v
	}
	return v
}
func (b *WorkCalendars) Coverage() []WorkCoverage {
	out := []WorkCoverage{}
	if b != nil {
		for _, y := range b.years {
			out = append(out, WorkCoverage{y.Region, y.From, y.To, y.Version, y.Source, y.Published})
		}
	}
	return out
}

// Immutable bundled data is shared by pure rule evaluation. Services validate
// this at startup; tests can supply a separate book without global mutation.
var bundledCalendars, bundledCalendarError = LoadWorkCalendars()

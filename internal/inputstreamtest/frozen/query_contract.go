package frozen

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/inputstream"
)

// These types describe the historical four-tool query contract.
type Projection struct {
	inputstream.Projection
	Scopes []Scope
	Text   string
}

const MaxSearchBytes = 1024

var ErrScopeRequired = errors.New("input_scope_required")
var ErrAmbiguous = errors.New("input_scope_ambiguous")

type Scope struct {
	Key   string
	Label string
}

type NativeIndex interface {
	Clone() NativeIndex
	Add(StoredRecord) error
	CompileInputFilter(FilterContext, json.RawMessage) (Selection, error)
}
type FilterContext struct {
	ScopeKey     string
	Snapshot     Snapshot
	Allowed      []Location // Already member/scope/snapshot checked. Never credential data.
	IndexVersion int
}
type Selection struct {
	Entries       []Location
	Missing       int
	ResolvedScope string
}
type Guidance struct {
	Version string `json:"-"`
	Text    string `json:"text"`
}

type ScopeOption struct {
	Ref   string `json:"scope_ref"`
	Label string `json:"label"`
}
type ScopeError struct {
	Reason  error
	Options []ScopeOption
	Native  []Scope
}

func (e *ScopeError) Error() string {
	b, _ := json.Marshal(struct {
		Code    string        `json:"code"`
		Options []ScopeOption `json:"options,omitempty"`
	}{e.Reason.Error(), e.Options})
	return string(b)
}
func (e *ScopeError) Unwrap() error { return e.Reason }

type GuidanceError struct {
	Reason   error
	Guidance Guidance
}

func (e *GuidanceError) Error() string {
	b, _ := json.Marshal(struct {
		Code     string   `json:"code"`
		Guidance Guidance `json:"guidance"`
	}{e.Reason.Error(), e.Guidance})
	return string(b)
}
func (e *GuidanceError) Unwrap() error { return e.Reason }

func ValidateFilter(data json.RawMessage) error {
	if len(data) == 0 {
		return nil
	}
	if len(data) > 4096 || !utf8.Valid(data) {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return ErrInvalid
	}
	if err := filterContainer(d, '{', 1); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}

func filterContainer(d *json.Decoder, kind json.Delim, depth int, maximum ...int) error {
	limit := 4
	if len(maximum) > 0 {
		limit = maximum[0]
	}
	if depth > limit {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for d.More() {
		if kind == '{' {
			token, err := d.Token()
			if err != nil {
				return ErrInvalid
			}
			key, ok := token.(string)
			if !ok || seen[key] {
				return ErrInvalid
			}
			seen[key] = true
		}
		token, err := d.Token()
		if err != nil || token == nil {
			return ErrInvalid
		}
		if delim, ok := token.(json.Delim); ok {
			if delim != '{' && delim != '[' {
				return ErrInvalid
			}
			if err := filterContainer(d, delim, depth+1, limit); err != nil {
				return err
			}
		}
	}
	end, err := d.Token()
	expected := json.Delim('}')
	if kind == '[' {
		expected = ']'
	}
	if err != nil || end != expected {
		return ErrInvalid
	}
	return nil
}

func DecodeFilter(b json.RawMessage, target any) error {
	if err := ValidateFilter(b); err != nil {
		return err
	}
	if len(b) == 0 {
		b = json.RawMessage(`{}`)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil {
		return ErrInvalid
	}
	for _, v := range fields {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return ErrInvalid
		}
	}
	return StrictDecode(b, target)
}

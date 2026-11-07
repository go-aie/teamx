package room

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// Diagnostics supplement existing sentinels; HTTP status and errors.Is contracts
// stay unchanged. Messages and hints are authored here, never remote error text.
type requestDiagnostic struct {
	cause           error
	Message         string
	Field           string
	ExpectedVersion *uint64
	ActualVersion   *uint64
	Hint            string
}

func (e *requestDiagnostic) Error() string {
	message := e.Message
	if e.Field != "" {
		message = e.Field + ": " + message
	}
	if e.ExpectedVersion != nil {
		message += fmt.Sprintf(" (expected=%d actual=%d)", *e.ExpectedVersion, *e.ActualVersion)
	}
	if e.Hint != "" {
		message += "; " + e.Hint
	}
	return e.cause.Error() + ": " + message
}
func (e *requestDiagnostic) Unwrap() error { return e.cause }
func invalidField(field, message string) error {
	return &requestDiagnostic{cause: ErrRequest, Field: field, Message: message}
}
func conflictReason(field, message, hint string) error {
	return &requestDiagnostic{cause: ErrConflict, Field: field, Message: message, Hint: hint}
}
func versionConflict(field string, expected, actual uint64, hint string) error {
	return &requestDiagnostic{cause: ErrConflict, Message: "observed version does not match current version", Field: field, ExpectedVersion: &expected, ActualVersion: &actual, Hint: hint}
}

// This decoder preserves the existing strict JSON acceptance rules. Values are
// never echoed: type errors report only their path, expected type and offset.
func decodeRequestJSON(payload string, target any) error {
	dec := json.NewDecoder(strings.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		var typeErr *json.UnmarshalTypeError
		var syntaxErr *json.SyntaxError
		var timestampErr *time.ParseError
		var authored *requestDiagnostic
		switch {
		case errors.As(err, &authored):
			return err
		case errors.As(err, &typeErr):
			path := typeErr.Field
			if path == "" {
				path = jsonErrorPath(json.RawMessage(payload), reflect.TypeOf(target), "", "")
			}
			return invalidField(path, fmt.Sprintf("expected %s at byte %d", typeErr.Type, typeErr.Offset))
		case errors.As(err, &syntaxErr):
			return invalidField("", fmt.Sprintf("%s at byte %d", syntaxErr.Error(), syntaxErr.Offset))
		case errors.As(err, &timestampErr):
			path := jsonErrorPath(json.RawMessage(payload), reflect.TypeOf(target), "", "")
			return invalidField(path, "expected RFC3339 timestamp")
		case errors.Is(err, io.EOF):
			return invalidField("", "missing JSON request")
		case errors.Is(err, io.ErrUnexpectedEOF):
			return invalidField("", "incomplete JSON request")
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			name, _ := strconv.Unquote(strings.TrimPrefix(err.Error(), "json: unknown field "))
			path := jsonErrorPath(json.RawMessage(payload), reflect.TypeOf(target), name, "")
			if path == "" {
				path = name
			}
			// Unusual field names may contain control characters or pasted secrets.
			if len(path) > 256 || strings.IndexFunc(path, func(r rune) bool {
				return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.-[]", r))
			}) >= 0 {
				path = ""
			}
			return invalidField(path, "unknown JSON field")
		default:
			// Custom decoders (e.g. time.Time) may include the submitted value.
			return invalidField("", "invalid JSON value (check field types and RFC3339 timestamps)")
		}
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return invalidField("", "expected exactly one JSON value; unexpected trailing data")
	}
	return nil
}

// Locate an unknown field or malformed timestamp only after the strict decoder
// rejected it. This does not validate or transform requests. Embedded DTOs keep
// the current JSON field names.
func jsonErrorPath(data json.RawMessage, typ reflect.Type, unknown, prefix string) string {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeFor[time.Time]() {
		var timestamp time.Time
		if unknown == "" && json.Unmarshal(data, &timestamp) != nil {
			return prefix
		}
		return ""
	}
	if typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		var values []json.RawMessage
		if json.Unmarshal(data, &values) == nil {
			for i, value := range values {
				if path := jsonErrorPath(value, typ.Elem(), unknown, fmt.Sprintf("%s[%d]", prefix, i)); path != "" {
					return path
				}
			}
		}
		return ""
	}
	if typ.Kind() != reflect.Struct {
		if unknown == "" && json.Unmarshal(data, reflect.New(typ).Interface()) != nil {
			return prefix
		}
		return ""
	}
	fields := map[string]reflect.Type{}
	var collect func(reflect.Type)
	collect = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
				collect(f.Type)
				continue
			}
			if f.PkgPath != "" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			fields[name] = f.Type
		}
	}
	collect(typ)
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil {
		return ""
	}
	for name, value := range object {
		fieldType, known := fields[name]
		if !known {
			for key, t := range fields {
				if strings.EqualFold(key, name) {
					fieldType, known = t, true
					break
				}
			}
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		if !known && name == unknown {
			return path
		}
		if known {
			if found := jsonErrorPath(value, fieldType, unknown, path); found != "" {
				return found
			}
		}
	}
	return ""
}

// Prefix only boundary field names, without changing the wrapped business code.
func prefixDiagnostic(err error, prefix string) error {
	var diagnostic *requestDiagnostic
	if errors.As(err, &diagnostic) && diagnostic.Field != "" {
		copy := *diagnostic
		copy.Field = prefix + copy.Field
		return &copy
	}
	return err
}

// Prefer structured details from a paired server, retaining support for older
// responses containing only code/error. No retries are performed here.
func (e apiError) cliError() error {
	message := e.Message
	if message == "" {
		return fmt.Errorf("%s: %s", e.Code, e.Error)
	}
	if e.Field != "" {
		message = e.Field + ": " + message
	}
	if e.ExpectedVersion != nil && e.ActualVersion != nil {
		message += fmt.Sprintf(" (expected=%d actual=%d)", *e.ExpectedVersion, *e.ActualVersion)
	}
	if e.Hint != "" {
		message += "; " + e.Hint
	}
	return fmt.Errorf("%s: %s", e.Code, message)
}

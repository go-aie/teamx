package room

import (
	"bytes"
	"errors"
	"reflect"
	"slices"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// Replace only expressions belonging to the root models subtree. Keep unrelated
// bytes and comments, including quoted/dotted keys, inline and multiline values.
func patchModelCatalog(source []byte, catalog modelCatalog) ([]byte, error) {
	if err := catalog.validate(); err != nil {
		return nil, err
	}
	var before map[string]any
	if err := toml.Unmarshal(source, &before); err != nil {
		return nil, errors.New("cannot parse startup config")
	}
	next, err := removeConfigSubtrees(source, []string{"models"})
	if err != nil {
		return nil, err
	}
	rendered, err := toml.Marshal(struct {
		Models modelCatalog `toml:"models"`
	}{catalog})
	if err != nil {
		return nil, err
	}
	next = append(next, rendered...)
	var decoded startupConfig
	var after map[string]any
	if len(next) > 1<<20 || toml.NewDecoder(bytes.NewReader(next)).DisallowUnknownFields().Decode(&decoded) != nil || decoded.Models == nil || !reflect.DeepEqual(*decoded.Models, catalog) || toml.Unmarshal(next, &after) != nil {
		return nil, errors.New("cannot safely save model catalog")
	}
	delete(before, "models")
	delete(after, "models")
	if len(before) == 0 && len(after) == 0 {
		return next, nil
	}
	if !reflect.DeepEqual(before, after) {
		return nil, errors.New("model save changed unrelated configuration")
	}
	return next, nil
}

// Remove complete TOML subtrees without rewriting unrelated fields or comments.
func removeConfigSubtrees(source []byte, paths ...[]string) ([]byte, error) {
	matches := func(path []string) bool {
		for _, prefix := range paths {
			if len(path) >= len(prefix) && slices.Equal(path[:len(prefix)], prefix) {
				return true
			}
		}
		return false
	}
	var parser unstable.Parser
	parser.Reset(source)
	type span struct {
		start, end  int
		replacement []byte
	}
	var spans []span
	var table []string
	keys := func(n *unstable.Node) []string {
		var path []string
		it := n.Key()
		for it.Next() {
			path = append(path, string(it.Node().Data))
		}
		return path
	}
	for parser.NextExpression() {
		n := parser.Expression()
		switch n.Kind {
		case unstable.Table, unstable.ArrayTable:
			table = keys(n)
			if matches(table) {
				first := n.Child()
				last := first
				for last.Next() != nil {
					last = last.Next()
				}
				start := int(first.Raw.Offset)
				for start > 0 && source[start] != '[' {
					start--
				}
				if n.Kind == unstable.ArrayTable {
					start--
				}
				end := int(last.Raw.Offset + last.Raw.Length)
				for end < len(source) && source[end] != ']' {
					end++
				}
				end++
				if n.Kind == unstable.ArrayTable {
					end++
				}
				if start < 0 || end > len(source) {
					return nil, ErrInvalid
				}
				spans = append(spans, span{start: start, end: end})
			}
		case unstable.KeyValue:
			path := append(slices.Clone(table), keys(n)...)
			if matches(path) {
				spans = append(spans, span{start: int(n.Raw.Offset), end: int(n.Raw.Offset + n.Raw.Length)})
				continue
			}
			// An inline parent must be patched as one expression. Keep the
			// surrounding table scope and encode the replacement inline too.
			var relative [][]string
			for _, prefix := range paths {
				if len(prefix) > len(path) && slices.Equal(prefix[:len(path)], path) {
					relative = append(relative, prefix[len(path):])
				}
			}
			if len(relative) > 0 {
				raw := source[n.Raw.Offset : n.Raw.Offset+n.Raw.Length]
				var value map[string]any
				if toml.Unmarshal(raw, &value) != nil {
					return nil, ErrInvalid
				}
				for _, rest := range relative {
					cursor := value
					for _, key := range append(keys(n), rest...)[:len(keys(n))+len(rest)-1] {
						cursor, _ = cursor[key].(map[string]any)
						if cursor == nil {
							break
						}
					}
					if cursor != nil {
						delete(cursor, rest[len(rest)-1])
					}
				}
				var replacement bytes.Buffer
				if err := toml.NewEncoder(&replacement).SetTablesInline(true).Encode(value); err != nil {
					return nil, err
				}
				spans = append(spans, span{start: int(n.Raw.Offset), end: int(n.Raw.Offset + n.Raw.Length), replacement: bytes.TrimSuffix(replacement.Bytes(), []byte("\n"))})
			}
		}
	}
	if parser.Error() != nil {
		return nil, errors.New("cannot parse startup config")
	}
	var next []byte
	offset := 0
	for _, r := range spans {
		if r.start < offset || r.end > len(source) {
			return nil, ErrInvalid
		}
		next = append(next, source[offset:r.start]...)
		next = append(next, r.replacement...)
		offset = r.end
	}
	next = append(next, source[offset:]...)
	if len(next) > 0 && next[len(next)-1] != '\n' {
		next = append(next, '\n')
	}
	return next, nil
}

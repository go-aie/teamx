// Package platform contains tool declarations and notification identity values.
package platform

// Tool declares an accountless executable. Executable is a server-side program
// name or path, resolved only at runtime. Registration grants no member access.
// The tool receives arguments and stdin through TeamX's existing process runner.
type Tool struct {
	Name        string
	Description string
	Version     string
	Executable  string
}

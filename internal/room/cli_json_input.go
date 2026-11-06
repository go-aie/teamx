package room

import (
	"io"
	"os"
)

func referencePayloadFlag(command, op string) string {
	switch command + "/" + op {
	case "actions/execute":
		return "request"
	case "policy/update", "matters/update", "matters/notify", "matters/reply", "matters/instruct", "matters/instruction-status":
		return "change"
	default:
		return ""
	}
}

func readCLIJSONFile(path string, stdin io.Reader) (string, error) {
	if path == "" {
		return "", invalidField("", "file path is required; use - to read stdin")
	}
	source := stdin
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer file.Close()
		source = file
	}
	// Match inline input: decode locally, then let the unchanged server enforce
	// the serialized envelope and business field limits. A raw source limit would
	// reject previously valid JSON containing extra whitespace or escape sequences.
	data, err := io.ReadAll(source)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

package scion

import (
	"net/url"
	"strings"
)

// --version is a value-taking self-update option as well as a global flag.
// Only an unambiguous global version request may skip account admission.
func Help(args []string) bool {
	if len(args) == 0 {
		return true
	}
	if args[0] == "help" || args[0] == "version" {
		return true
	}
	globalOnly, version := true, false
	for _, arg := range args {
		switch arg {
		case "--":
			return false
		case "--help", "-h":
			return true
		case "--version":
			version = true
		case "--json", "--quiet", "-q":
		default:
			globalOnly = false
		}
	}
	return globalOnly && version
}

// ValidateArguments constrains credential destinations and operator-only CLI
// configuration. It does not translate native commands or govern permissions.
// Native clap remains responsible for command syntax and output.
func ValidateArguments(origin string, args []string) error {
	command := ""
	addressCommands := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		// clap permits clustered global short options, e.g. -qC PATH.
		// C consumes the rest of the token or the following argument.
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") {
			if at := strings.IndexByte(arg, 'C'); at >= 1 && strings.Trim(arg[1:at], "q") == "" {
				if at == len(arg)-1 {
					i++
					if i >= len(args) {
						return failure("invalid_response")
					}
				}
				continue
			}
		}
		if arg == "--" {
			// Positional repository URLs after -- can still replace the endpoint.
			if addressCommands {
				for _, value := range args[i+1:] {
					if !validTarget(origin, value) {
						return failure("identity_mismatch")
					}
				}
			}
			break
		}
		key, value, equal := strings.Cut(arg, "=")
		switch key {
		case "--endpoint", "--hub", "--repo", "-C":
			if !equal {
				i++
				if i >= len(args) {
					return failure("invalid_response")
				}
				value = args[i]
			}
			if key == "--endpoint" || key == "--hub" {
				if value != origin {
					return failure("identity_mismatch")
				}
			} else if key == "--repo" && !validTarget(origin, value) {
				return failure("identity_mismatch")
			}
			continue
		}
		if command == "" && !strings.HasPrefix(arg, "-") {
			command = arg
			switch command {
			case "login", "logout", "token", "config", "daemon", "self-update":
				return failure("invalid_profile")
			case "repo", "org", "clone", "remote", "doctor", "whoami":
				addressCommands = true
			}
		} else if addressCommands && !validTarget(origin, valueOrArgument(arg)) {
			return failure("identity_mismatch")
		}
	}
	return nil
}

func valueOrArgument(arg string) string {
	if strings.HasPrefix(arg, "-") {
		if _, value, ok := strings.Cut(arg, "="); ok {
			return value
		}
	}
	return arg
}

func validTarget(origin, value string) bool {
	if !strings.Contains(value, "://") {
		return true
	}
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	return u.Scheme+"://"+u.Host == origin
}

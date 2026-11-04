// Package gitlab provides TeamX built-in GitLab connections, notifications and
// Workspace Git preparation independently of agent tools.
package gitlab

import (
	"crypto/sha256"
	"embed"
	"fmt"
)

// Only runtime resources are distributed. Existing room copies stay frozen.
//
//go:embed tools/git-askpass.sh tools/USAGE.md tools/git-only/USAGE.md
var resources embed.FS

// TC-02 shipped these immutable files to Room-owned directories. Recognizing
// their exact bytes preserves existing Git/askpass resources without shipping
// or reinstalling the retired agent implementation. Unknown edits still fail.
var frozenTC02Resources = map[string]string{
	"tools/gitlab.py":          "ce6ed8a05f727959b7a596b0a870138297799bae06e04832e816286135e7f2e9",
	"tools/USAGE.md":           "7794a498488e1e50447fc71624dcd04154dd408afe2579da78953090186e36b4",
	"tools/git-only/gitlab.py": "d073cf2f898a862c8428b6eb3398aff9dca0039d3abef9fef776e4d58c15dfb8",
	"tools/git-only/USAGE.md":  "222de6a6fb71868eb43d9b968b80929e5653ff47224fc498af410383b00be303",
}

var frozenTC03Resources = map[string]string{
	"tools/gitlab.py":               "40bb064dca229f1a1c83b513b73c65fced4fe3f6a273fcba44a08e8e36520afe",
	"tools/git-only/gitlab.py":      "8a79108bc66c103f3b4fd7a39d3f70fba957fef05204a3cd6db540bf3e0523ef",
	"tools/git-askpass.sh":          "c50c09a51d40586014efba3ce80789f9017f004ddbb5c662309fb02e37d7a8ed",
	"tools/git-only/git-askpass.sh": "c50c09a51d40586014efba3ce80789f9017f004ddbb5c662309fb02e37d7a8ed",
	"tools/USAGE.md":                "3c3e2c3b05f3fbbec430c52a6d761cfe0bf674fc7fa9f0bda7244c9eb6a9feec",
	"tools/git-only/USAGE.md":       "dfb8f5330cf7d954341577a6e0fe4fa3884f53c3cb6eea3445b01146d6affffa",
}

func knownFrozenResource(name string, data []byte) bool {
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	return frozenTC02Resources[name] == digest || frozenTC03Resources[name] == digest
}

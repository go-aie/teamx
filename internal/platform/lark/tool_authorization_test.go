package lark

import "testing"

func TestToolAuthorizationRequiredByIntentAndPlatformSupport(t *testing.T) {
	for _, tc := range []struct {
		name, capability, state, scope string
		app                            bool
		operations                     []string
		want                           bool
	}{
		{"read gap", "message.read", "blocked", "im:message:readonly", true, nil, true},
		{"reply gap", "message.reply", "blocked", "im:message", true, []string{"message.reply"}, true},
		{"send gap", "message.send", "blocked", "im:message", true, []string{"message.send"}, true},
		{"unsupported pin", "message.send", "unsupported", "im:message", true, []string{"message.send"}, false},
		{"app gap", "message.reply", "blocked", "im:message", false, []string{"message.reply"}, false},
		{"unrequested target scope", "message.send", "blocked", "im:chat.members:read", true, []string{"message.reply"}, false},
		{"self target scope", "message.send_self", "blocked", "im:chat.members:read", true, []string{"message.send_self"}, true},
		{"renewal", "token.renew", "blocked", "offline_access", false, nil, true},
		{"no gap", "message.read", "ready", "", true, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cap := Capability{Name: tc.capability, State: tc.state}
			if tc.scope != "" {
				cap.MissingUserScopes = []string{tc.scope}
			}
			check := PermissionCheck{Capabilities: []Capability{cap}}
			if tc.app {
				check.Application.Granted = []string{tc.scope}
			}
			if got := ToolAuthorizationRequired(check, tc.operations); got != tc.want {
				t.Fatal(got, tc.want)
			}
		})
	}
}

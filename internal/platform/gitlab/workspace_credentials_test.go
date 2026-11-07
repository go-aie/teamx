package gitlab

import (
	"context"
	"testing"
)

func TestAskpassValidatesRepositoryBeforeCredentials(t *testing.T) {
	b := eventTestBinding()
	calls := 0
	env := func(k string) string {
		calls++
		if k == "TEAMX_PLATFORM_TOKEN" {
			return "offline-secret"
		}
		return ""
	}
	for _, prompt := range []string{"", "Password for 'https://evil.invalid/group/project.git': ", "Password for 'https://gitlab.example.com/group/other.git': ", "Password for 'https://oauth2:bad@gitlab.example.com/group/project.git': ", "Password for 'https://gitlab.example.com/group/project.git?': ", "Password for 'https://gitlab.example.com/group/project.git#x': ", "Password for 'https://gitlab.example.com/group': "} {
		if _, err := Askpass(context.Background(), b, prompt, env, nil); err == nil {
			t.Fatal("accepted foreign target")
		}
	}
	if calls != 0 {
		t.Fatal("resolved credential for invalid target")
	}
	answer, err := Askpass(context.Background(), b, "Username for 'https://gitlab.example.com/group/project.git': ", env, nil)
	if err != nil || answer != "oauth2" || calls != 0 {
		t.Fatal(err)
	}
	answer, err = Askpass(context.Background(), b, "Password for 'https://oauth2@gitlab.example.com/group/project.git': ", env, nil)
	if err != nil || answer != "offline-secret" || calls != 1 {
		t.Fatal("password resolution", err)
	}
}

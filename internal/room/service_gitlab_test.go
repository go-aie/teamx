//go:build darwin || linux

package room

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceUpgradePreservesGitLabApplications(t *testing.T) {
	const catalog = `{"version":1,"applications":[{"id":"app","name":"Company","origin":"https://gitlab.example","application_version":1,"status":"active","secret":"private-app-secret"}]}`
	for _, change := range []string{"preserved", "removed", "empty", "instance", "version", "secret", "permissions"} {
		t.Run(change, func(t *testing.T) {
			r := serviceRegistration{Home: t.TempDir(), UID: os.Getuid()}
			path := filepath.Join(r.Home, "gitlab-applications.json")
			serviceTestFile(t, path, catalog, 0600)
			check, err := preserveServiceGitLabApplications(r)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "preserved":
				serviceTestFile(t, path, "\n"+catalog+"\n", 0600)
			case "removed":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "empty":
				serviceTestFile(t, path, `{"version":1,"applications":[]}`, 0600)
			case "instance":
				serviceTestFile(t, path, strings.ReplaceAll(catalog, "gitlab.example", "other.example"), 0600)
			case "version":
				serviceTestFile(t, path, strings.ReplaceAll(catalog, `"application_version":1`, `"application_version":2`), 0600)
			case "secret":
				serviceTestFile(t, path, strings.ReplaceAll(catalog, "private-app-secret", "replacement-secret"), 0600)
			case "permissions":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			err = check()
			if (err == nil) != (change == "preserved") {
				t.Fatalf("unexpected preservation result: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("private catalog leaked in diagnostic")
			}
		})
	}
}

func TestServiceUpgradeAllowsUnconfiguredGitLab(t *testing.T) {
	r := serviceRegistration{Home: t.TempDir(), UID: os.Getuid()}
	check, err := preserveServiceGitLabApplications(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := check(); err != nil {
		t.Fatal(err)
	}
	serviceTestFile(t, filepath.Join(r.Home, "gitlab-applications.json"), `{"version":1,"applications":[]}`, 0600)
	if err := check(); err != nil {
		t.Fatal(err)
	}
}

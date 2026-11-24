//go:build linux

package srt

import (
	"errors"
	"os"
	"strings"
	"syscall"
)

func hostCheck() error {
	if os.Geteuid() == 0 {
		return errors.New("SRT execution refuses a root core")
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(status), "\n") {
		if name, value, ok := strings.Cut(line, ":"); ok {
			fields[name] = strings.TrimSpace(value)
		}
	}
	if fields["CapEff"] == "" || fields["CapBnd"] == "" || strings.Trim(fields["CapEff"], "0") != "" || strings.Trim(fields["CapBnd"], "0") != "" || fields["NoNewPrivs"] != "1" {
		return errors.New("SRT core requires empty capabilities and no-new-privileges")
	}
	return nil
}
func multipleLinks(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return !ok || stat.Nlink > 1
}

func checkWorkspaceMounts(workspace string) error {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return err
	}
	return checkMountInfo(workspace, string(data))
}

// Path hiding cannot protect private inodes already exposed by a bind alias.
func checkMountInfo(workspace, data string) error {
	type mount struct{ device, root, point string }
	var mounts []mount
	unescape := strings.NewReplacer(`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	for _, line := range strings.Split(strings.TrimSpace(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 7 {
			return errors.New("SRT cannot verify workspace mount identity")
		}
		mounts = append(mounts, mount{fields[2], unescape.Replace(fields[3]), unescape.Replace(fields[4])})
	}
	for _, m := range mounts {
		if within(workspace, m.point) {
			return errors.New("SRT workspace contains an unapproved mount")
		}
		if m.point == "/" || !within(m.point, workspace) {
			continue
		}
		if m.root != "/" {
			return errors.New("SRT workspace is reached through a mount alias")
		}
		for _, other := range mounts {
			if other.point != m.point && other.device == m.device && other.root == m.root {
				return errors.New("SRT workspace filesystem has a mount alias")
			}
		}
	}
	return nil
}

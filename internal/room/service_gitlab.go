//go:build darwin || linux

package room

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/go-aie/teamx/internal/platform/gitlaboauth"
)

// Capture while the service is stopped. Application secrets stay in memory;
// neither the upgrade receipt nor diagnostics include the private catalog.
func preserveServiceGitLabApplications(r serviceRegistration) (func() error, error) {
	path := filepath.Join(r.Home, "gitlab-applications.json")
	var before gitlaboauth.Snapshot
	err := readIdentitySnapshotForUID(path, &before, uint32(r.UID))
	if errors.Is(err, os.ErrNotExist) {
		// A deployment may enable GitLab before configuring its first application.
		return func() error { return nil }, nil
	}
	if err != nil {
		return nil, errors.New("无法读取 GitLab 服务应用配置；请检查文件属主、权限和内容")
	}
	return func() error {
		var after gitlaboauth.Snapshot
		if err := readIdentitySnapshotForUID(path, &after, uint32(r.UID)); err != nil || !reflect.DeepEqual(before, after) {
			return errors.New("升级后 GitLab 服务应用配置缺失、不可读或已变化；请保留备份并检查原部署 Home")
		}
		return nil
	}, nil
}

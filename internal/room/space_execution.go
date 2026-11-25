package room

import (
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/go-aie/teamx/internal/agent/srt"
)

// Existing Space launchers are pinned to physical releases. An upgrade of the
// host must retain their deployment-owned execution descriptor as well. New
// Spaces use the current descriptor. No request or Space setting can supply a
// manifest or expand the deployment's endpoint/private-root authority.
func retainedSpaceExecutionConfig(deployment, space startupConfig) (*srt.Config, error) {
	if deployment.Deployment.Execution.Kind != "srt" || space.Runtime == nil {
		return nil, nil
	}
	current, err := srt.ReadConfig(deployment.Deployment.Execution.Manifest)
	if err != nil {
		return nil, err
	}
	release := filepath.Dir(filepath.Dir(current.Runtime.Path))
	// Standalone/source SRT compositions keep their explicitly supplied factory;
	// only managed installs have archived release descriptors to select from.
	if filepath.Base(filepath.Dir(release)) != "releases" || current.Runtime.Path != filepath.Join(release, "runtime/srt") {
		return nil, nil
	}
	binary, err := filepath.EvalSymlinks(space.Runtime.DSH.BinaryPath)
	if err != nil {
		return nil, err
	}
	if pathWithin(release, binary) {
		return nil, nil
	}
	previous := filepath.Dir(filepath.Dir(binary))
	if filepath.Dir(previous) != filepath.Dir(release) || binary != filepath.Join(previous, "bin", "dsh") {
		return nil, errors.New("Space DSH is outside installed execution releases")
	}
	path := filepath.Join(filepath.Dir(deployment.Deployment.Execution.Manifest), filepath.Base(previous)+".json")
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("retained execution descriptor must be a private regular file")
	}
	retained, err := srt.ReadConfig(path)
	if err != nil {
		return nil, err
	}
	if retained.Node.Path != filepath.Join(previous, "runtime/node/bin/node") || retained.Runtime.Path != filepath.Join(previous, "runtime/srt") ||
		!slices.Equal(retained.Endpoints, current.Endpoints) || !slices.Equal(retained.PrivateRoots, current.PrivateRoots) {
		return nil, errors.New("retained execution descriptor differs from deployment authority")
	}
	// Start/Identify still verify every original artifact digest and the exact
	// supervisor before execution; an unavailable old runtime never falls back.
	protectSRTDeploymentRoots(&retained, deployment)
	return &retained, nil
}

func protectSRTDeploymentRoots(config *srt.Config, deployment startupConfig) {
	config.PrivateRoots = append(config.PrivateRoots, filepath.Dir(deployment.path))
	if deployment.Server.DataDir != nil {
		config.PrivateRoots = append(config.PrivateRoots, *deployment.Server.DataDir)
	}
}

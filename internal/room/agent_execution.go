package room

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-aie/teamx/internal/agent/srt"
)

// Deployment assembly is the only place that knows the chosen implementation.
// Space settings and Agent requests cannot choose a launcher or weaken policy.
func selectAgentExecution(ctx context.Context, c *startupConfig, plannedEndpoint ...string) error {
	if err := checkDeploymentRoot(*c); err != nil {
		return err
	}
	if c.Deployment.Mode != "" && c.Deployment.Mode != "personal" {
		if c.Deployment.Mode != "multi-user" {
			return errors.New("unsupported deployment mode")
		}
		if runtime.GOOS != "linux" {
			return errors.New("multi-user deployment requires Linux")
		}
		if c.Deployment.Execution.Kind != "srt" {
			return errors.New("multi-user deployment requires SRT execution")
		}
		if c.Deployment.Authentication.Provider == "" || !c.Deployment.Authentication.AllowAllAuthenticated && len(c.Deployment.Authentication.AllowedSubjects) == 0 {
			return errors.New("multi-user deployment requires an authentication provider and explicit admission policy")
		}
		// Development installations may exercise the complete hosted combination
		// before release acceptance. Keep the release gate independent of runtime
		// settings, and continue through every SRT check below.
		if !multiUserDevelopmentBuild(distributionVersion) {
			return errors.New("multi-user deployment is not available in release builds before execution/toolchain and authentication acceptance")
		}
	}
	switch c.Deployment.Execution.Kind {
	case "", "native":
		if c.Deployment.Execution.Manifest != "" {
			return errors.New("native execution does not accept an SRT manifest")
		}
		c.execution = nil // original native execution; no SRT IO or initialization
		return nil
	case "srt":
		if c.Access.LoginEnabled != nil && !*c.Access.LoginEnabled {
			return errors.New("isolated execution requires authenticated access")
		}
		if !filepath.IsAbs(c.Deployment.Execution.Manifest) {
			return errors.New("SRT execution requires an absolute deployment manifest")
		}
		config, err := srt.ReadConfig(c.Deployment.Execution.Manifest)
		if err != nil {
			return err
		}
		protectSRTDeploymentRoots(&config, *c)
		selected := srt.New(config)
		if err := selected.Check(ctx); err != nil {
			return err
		}
		for _, raw := range plannedEndpoint {
			if _, err := selected.RouteEndpoint(raw); err != nil {
				return err
			}
			// Port zero cannot be part of stable recovery/configuration.
			if strings.HasSuffix(raw, ":0") {
				return errors.New("SRT requires a stable internal callback port")
			}
			found := false
			for _, approved := range config.Endpoints {
				a, _ := url.Parse(approved)
				b, _ := url.Parse(raw)
				if a != nil && b != nil && a.Scheme == b.Scheme && a.Host == b.Host {
					found = true
				}
			}
			if !found {
				return errors.New("internal callback endpoint is outside SRT deployment authority")
			}
		}
		c.execution = selected
		return nil
	default:
		return errors.New("unsupported Agent execution implementation")
	}
}

func multiUserDevelopmentBuild(version string) bool {
	return version == "dev" || strings.HasPrefix(version, "dev-")
}

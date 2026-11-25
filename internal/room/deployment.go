package room

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/authn"
	"github.com/go-aie/teamx/internal/platform/lark"
)

// Selection contains declarations only. Validation must precede home creation,
// identity registration, credential access, listeners and recovery workers.
// Hosted development acceptance additionally requires the Linux/SRT and explicit
// account admission checks in selectAgentExecution; release builds stay gated.
type deploymentComposition struct {
	factories spaceFactories
	spaces    *spaceServer  // retained through incomplete multi-space settlement
	runtime   *SpaceRuntime // initial default assembly; multi-space shutdown is owned by spaces
}

type spaceFactories struct {
	// Bound by deployment composition, never by Space settings or a request.
	// Installed release combinations still require their separate acceptance.
	hostedResetAccepted bool
	authentication      func(externalAuthConfig) (authn.Provider, error)
	hostTools           func(string) (hostToolPolicy, error)
	platformClients     func() *lark.ClientFactory
	service             func(string, string, serviceConfig) (*service, error)
	manager             func(string, string, uint64, *teamRuntime, *inputUpgradeOptions, *platforms.Catalog) (*roomManager, error)
	models              func(startupConfig) (*modelExecution, error)
	modelController     func(context.Context, *roomManager, startupConfig, func(context.Context, dsh.Options) (dsh.ProbeResult, error)) (*modelController, error)
	computer            func(context.Context, startupConfig, *systemSettings, ServiceInfo, string, string, func(MemberIdentity) (bool, error)) (*computerController, error)
	setup               func(context.Context, net.Listener, startupConfig, *systemSettings, *serverAccess, func(any) error, io.Writer, func(context.Context, dsh.Options) (dsh.ProbeResult, error)) error
	migration           func(context.Context, net.Listener, startupConfig, *serverAccess, func(any) error) error
}

func personalDeployment(create func(string, string, serviceConfig) (*service, error)) deploymentComposition {
	if create == nil {
		create = newConfiguredService
	}
	return deploymentComposition{factories: spaceFactories{
		hostedResetAccepted: runtime.GOOS == "linux" && multiUserDevelopmentBuild(distributionVersion),
		authentication:      func(c externalAuthConfig) (authn.Provider, error) { return c.provider() },
		hostTools:           deploymentHostTools,
		platformClients: func() *lark.ClientFactory {
			return lark.NewClientFactory(lark.AccountRequestInterval, time.Second, time.Minute)
		},
		service: create, manager: openTeamRoomManagerWithCatalog,
		models: loadModelExecution, modelController: newModelController,
		computer: newComputerController, setup: serveSetupRequired, migration: serveModelMigrationBlocked,
	}}
}

func (c deploymentComposition) validate() error {
	for _, binding := range []struct {
		name    string
		present bool
	}{
		{"host tools", c.factories.hostTools != nil},
		{"service", c.factories.service != nil}, {"manager", c.factories.manager != nil},
		{"models", c.factories.models != nil}, {"platform clients", c.factories.platformClients != nil}, {"model controller", c.factories.modelController != nil},
		{"computer", c.factories.computer != nil}, {"setup", c.factories.setup != nil},
		{"migration", c.factories.migration != nil},
	} {
		if !binding.present {
			return fmt.Errorf("deployment binding missing: %s", binding.name)
		}
	}
	return nil
}

var errSpaceStopped = errors.New("space runtime is stopped")

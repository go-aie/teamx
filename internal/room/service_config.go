package room

import (
	"context"
	"github.com/go-aie/teamx/agent"
	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform/gitlaboauth"
	"github.com/go-aie/teamx/internal/platform/notification"
	"io"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Configuration belongs to service assembly. DSH launch/model parameters stay
// concrete until an actual second backend defines its own configuration.
type serviceConfig struct {
	gitlabApplications gitlaboauth.Source
	hostTools          hostToolPolicy
	execution          agent.ExecutionFactory
	executionResources *executionResources
	directoryClaims    *spaceDirectoryClaims
	environment        []string
	ownerUserID        string // Explicit owner for non-HTTP component assembly; servers resolve the registered Space owner.
	platformCatalog    *platforms.Catalog
	calendars          *CalendarService

	workspaceProtected   []string
	checkWorkspace       func(string) error
	inputAdapters        map[notification.SourceID]notificationAdapter
	inputCapabilities    func(string) notification.Capabilities
	unifiedInputs        bool // Normal server composition. Legacy fixture services use the old format.
	inputUpgradeLog      io.Writer
	updates              *uiUpdateHub
	agentNames           *agentNameRegistry
	scheduling           schedulingMode
	modelStartup         *startupConfig
	modelProbe           func(context.Context, dsh.Options) (dsh.ProbeResult, error)
	models               *modelExecution
	modelTargets         map[string]modelSessionSnapshot
	access               *serverAccess
	settings             *systemSettings
	imageMaterials       imageMaterialConfig
	inboxMode            string
	inboxExpiry          notification.ExpiryPolicy
	workspaceOperationID string // verified managed configuration only
	endpoint             string
	cliPath              string
	mergeWindow          time.Duration
	dshTurnTimeout       time.Duration
	maxTurns             uint64
	persistentScheduling bool
	dshProfiles          map[string]dshProfile
	uiProfiles           []string
}

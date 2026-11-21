package room

import (
	"context"
	"io"
	"os"
	"sync"

	builtinplatforms "github.com/go-aie/teamx/builtin/platforms"
	"github.com/go-aie/teamx/extension"
	"github.com/go-aie/teamx/internal/app/extensions"
	"github.com/go-aie/teamx/internal/app/platforms"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Legacy regression fixtures explicitly select the complete catalog. Production
// constructors never import that selection, and selection tests use the actual
// configured constructors below with empty or custom catalogs.
var defaultPlatformCatalog = sync.OnceValue(func() *platforms.Catalog {
	selected, err := extension.Compose(builtinplatforms.Extensions()...)
	if err != nil {
		panic(err)
	}
	c, err := extensions.Catalog(selected)
	if err != nil {
		panic(err)
	}
	return c
})

func defaultPlatformTools() []tool.Definition {
	return defaultPlatformCatalog().Tools(func(name, override string) string { return tool.ResolveExecutable(name, os.Getenv(override)) })
}

func notificationCapabilities(provider string) notification.Capabilities {
	return defaultPlatformCatalog().NotificationCapabilities(provider)
}

func defaultNotificationAdapters(m *roomManager) map[notification.SourceID]notificationAdapter {
	catalog := defaultPlatformCatalog()
	if m != nil && m.platformCatalog != nil {
		catalog = m.platformCatalog
	}
	return notificationAdaptersFor(m, catalog)
}

func newRoomManager(dir string, maxTurns uint64) (*roomManager, error) {
	return openRoomManager(dir, maxTurns, nil)
}
func openRoomManager(dir string, maxTurns uint64, upgrade *inputUpgradeOptions) (*roomManager, error) {
	return openRoomManagerWithCatalog(dir, maxTurns, upgrade, defaultPlatformCatalog())
}
func newTeamRoomManager(dir, teams string, maxTurns uint64, defaults *teamRuntime) (*roomManager, error) {
	return openTeamRoomManager(dir, teams, maxTurns, defaults, nil)
}
func openTeamRoomManager(dir, teams string, maxTurns uint64, defaults *teamRuntime, upgrade *inputUpgradeOptions) (*roomManager, error) {
	return openTeamRoomManagerWithCatalog(dir, teams, maxTurns, defaults, upgrade, defaultPlatformCatalog())
}
func OpenStore(dir, roomID string) (*Store, error) { return openStore(dir, roomID, nil) }
func openStore(dir, roomID string, upgrade *inputUpgradeOptions) (*Store, error) {
	return openStoreWithCatalog(dir, roomID, upgrade, defaultPlatformCatalog())
}
func lockStore(dir, roomID string) (*Store, error) {
	return lockStoreWithCatalog(dir, roomID, defaultPlatformCatalog())
}
func fixtureServiceConfig(c serviceConfig) serviceConfig {
	if c.platformCatalog == nil {
		c.platformCatalog = defaultPlatformCatalog()
	}
	return c
}
func newService(dir, roomID string, c serviceConfig) (*service, error) {
	return newConfiguredService(dir, roomID, fixtureServiceConfig(c))
}
func startService(dir, roomID string, c serviceConfig, assemble func(*service, RoomSnapshot) error) (*service, error) {
	return startConfiguredService(dir, roomID, fixtureServiceConfig(c), assemble)
}
func runServer(ctx context.Context, args []string, out, stderr io.Writer, options serverOptions) error {
	if options.platformCatalog == nil {
		options.platformCatalog = defaultPlatformCatalog()
	}
	return runConfiguredServer(ctx, args, out, stderr, options)
}
func serve(args []string, out, stderr io.Writer) error {
	return serveWithFactory(args, out, stderr, nil)
}
func serveWithFactory(args []string, out, stderr io.Writer, create func(string, string, serviceConfig) (*service, error)) error {
	return serveWithCatalog(args, out, stderr, create, defaultPlatformCatalog())
}
func Run(args []string, out, stderr io.Writer) error {
	return RunWithCatalog(args, out, stderr, defaultPlatformCatalog())
}
func runCLI(ctx context.Context, args []string, out, stderr io.Writer, env func(string) string) error {
	return runCLIWithInput(ctx, args, os.Stdin, out, stderr, env)
}
func runCLIWithInput(ctx context.Context, args []string, in io.Reader, out, stderr io.Writer, env func(string) string) error {
	return runCLIWithCatalog(ctx, args, in, out, stderr, env, defaultPlatformCatalog())
}
func runPlatformsCLI(ctx context.Context, args []string, out, stderr io.Writer, env func(string) string) error {
	return runPlatformsCLIWithCatalog(ctx, args, out, stderr, env, defaultPlatformCatalog())
}
func runPlatformsUpgrade(args []string, out, stderr io.Writer) error {
	return runPlatformsUpgradeWithCatalog(args, out, stderr, defaultPlatformCatalog())
}
func runNotificationsUpgrade(args []string, out, stderr io.Writer) error {
	return runNotificationsUpgradeWithCatalog(args, out, stderr, defaultPlatformCatalog())
}
func runBundle(command string, args []string, out, stderr io.Writer) error {
	return runBundleWithCatalog(command, args, out, stderr, defaultPlatformCatalog())
}
func bundleEntry(args []string, out, stderr io.Writer) error {
	return bundleEntryWithCatalog(args, out, stderr, defaultPlatformCatalog())
}

func projectNotification(item notification.Item) notification.View {
	return (&Store{catalog: defaultPlatformCatalog()}).projectNotification(item)
}
func projectInboxEntry(e InboxEntry, source followupSourceSummary) inboxEntryView {
	return (&Store{catalog: defaultPlatformCatalog()}).projectInboxEntry(e, source)
}
func inboxContextOnly(e InboxEntry) bool {
	return (&Store{catalog: defaultPlatformCatalog()}).inboxContextOnly(e)
}
func projectAgentInboxRead(id MemberIdentity, page referenceInboxPage) (agentReferenceInboxPage, error) {
	return (&Store{catalog: defaultPlatformCatalog()}).projectAgentInboxRead(id, page)
}
func projectSource(e followupEvent, entry InboxEntry) agentSourceView {
	return (&Store{catalog: defaultPlatformCatalog()}).projectSource(e, entry)
}
func projectInputSource(e followupEvent, entry *InboxEntry) any {
	return (&Store{catalog: defaultPlatformCatalog()}).projectInputSource(e, entry)
}
func validOnboarding(op platformOnboarding) bool {
	return (&roomManager{platformCatalog: defaultPlatformCatalog()}).validOnboarding(op)
}

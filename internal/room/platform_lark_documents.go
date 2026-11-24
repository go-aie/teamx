package room

import (
	"context"

	"github.com/go-aie/teamx/internal/platform/lark"
)

// URL reads use the same exact entry, membership and revocation lifetime as
// message reads, without inventing an Inbox source or saved message reference.
func (m *roomManager) readLarkDocument(ctx context.Context, req toolsRequest, url string) (lark.DocumentResult, error) {
	ctx, err := m.withToolEntry(ctx, req)
	if err != nil {
		return lark.DocumentResult{}, err
	}
	req.Tool = "lark"
	m.mu.Lock()
	a, call, callCtx, err := m.preparePlatformCallLocked(ctx, req, false)
	m.mu.Unlock()
	if err != nil {
		return lark.DocumentResult{}, err
	}
	defer m.finishPlatformCall(call)
	if a.account.CLIProfile == nil {
		return lark.DocumentResult{}, ErrConflict
	}
	client, err := m.larkClients.New(*a.account.CLIProfile)
	if err != nil {
		return lark.DocumentResult{}, err
	}
	result, readErr := client.FetchDocument(callCtx, a.account.Identity, url)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.checkLarkReadLocked(callCtx, req, a.account); err != nil {
		return lark.DocumentResult{}, err
	}
	if readErr != nil {
		if err := m.recordLarkReadFailureLocked(a.account, readErr); err != nil {
			return lark.DocumentResult{}, err
		}
		return lark.DocumentResult{}, readErr
	}
	return result, nil
}

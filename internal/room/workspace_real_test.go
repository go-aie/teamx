//go:build darwin || linux

package room

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
)

var workspaceRealOutput = flag.String("teamx-workspace-real-output", "", "new absolute evidence directory; enables real GitLab/DSH Workspace acceptance")
var workspaceRealRepository = flag.String("teamx-workspace-repository", "", "existing HTTPS GitLab repository; acceptance makes no remote writes")
var workspaceBrowser = flag.Bool("teamx-workspace-browser", false, "wait for actual browser create/discussion/binding instead of DOM driver")

var workspacePreview = flag.Duration("teamx-workspace-preview", 0, "keep successful real server available for browser inspection")

func (d *realRoomDriver) realWorkspaceStatus(epoch string, room roomSummary) (workspaceResponse, error) {
	var result workspaceResponse
	q := url.Values{"service_epoch": {epoch}, "caller_id": {humanCaller.ID}, "room_id": {room.RoomID}, "room_instance": {room.RoomInstance}}
	req, err := http.NewRequestWithContext(d.ctx, http.MethodGet, d.endpoint+"/v1/workspace?"+q.Encode(), nil)
	if err != nil {
		return result, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return result, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return result, fmt.Errorf("workspace status HTTP %d", res.StatusCode)
	}
	err = json.NewDecoder(res.Body).Decode(&result)
	return result, err
}

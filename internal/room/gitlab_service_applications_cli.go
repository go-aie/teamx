package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlaboauth"
)

// Secret-bearing configuration is read from a private file, never a flag or a
// response. Administration uses the same local-only token gate as user status.
func runGitLabApplicationManagement(args []string, stdout, stderr io.Writer) error {
	action := "list"
	if len(args) > 0 && (args[0] == "list" || args[0] == "configure" || args[0] == "settle") {
		action = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("access gitlab-applications", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: teamx access gitlab-applications [list|configure|settle] --config FILE [--input FILE|--application ID]")
		flags.PrintDefaults()
	}
	path := flags.String("config", "", "deployment config.toml")
	inputPath := flags.String("input", "", "private 0600 application JSON file (configure)")
	id := flags.String("application", "", "application ID (settle)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 || *path == "" || action != "list" && action != "configure" && action != "settle" {
		return ErrRequest
	}
	c, err := readStartupConfig(*path, true, false)
	if err != nil {
		return err
	}
	if c.Deployment.Mode != "multi-user" || !c.Deployment.GitLab.Enabled {
		return errors.New("managed GitLab is not selected")
	}
	token, err := readAdminToken(adminTokenPath(c))
	if err != nil {
		return err
	}
	var data []byte
	if action == "configure" {
		if *inputPath == "" {
			return ErrRequest
		}
		info, err := os.Lstat(*inputPath)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 32<<10 {
			return errors.New("application input requires a private 0600 regular file")
		}
		data, err = os.ReadFile(*inputPath)
		if len(data) > 32<<10 {
			return ErrRequest
		}
		if err != nil {
			return err
		}
		var input struct {
			Application gitlaboauth.Application `json:"application"`
			Version     uint64                  `json:"version"`
		}
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
			return ErrRequest
		}
		if input.Application.ID == "" && input.Version == 0 {
			input.Application.ID, err = newOpaqueID()
			if err != nil {
				return err
			}
		}
		input.Application.Source = gitlaboauth.ServiceSource
		if input.Application.Status == "" {
			input.Application.Status = "active"
		}
		if input.Application.Scopes == nil {
			input.Application.Scopes = []string{"api"}
		}
		if input.Application.RedirectURI == "" {
			input.Application.RedirectURI = c.Server.PublicBaseURL + gitlabCallbackPath
		}
		data, err = json.Marshal(input)
		if err != nil {
			return err
		}
	} else if action == "settle" {
		if !validSpaceID(*id) {
			return ErrRequest
		}
		data, _ = json.Marshal(map[string]any{"settle": true, "application": map[string]string{"id": *id}})
	}
	listen := "127.0.0.1:8088"
	if c.Server.Listen != nil {
		listen = *c.Server.Listen
	}
	method := http.MethodGet
	var body io.Reader
	if data != nil {
		method = http.MethodPost
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, internalAccessEndpoint(listen)+"/v1/deployment/gitlab-applications", body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 40 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("local application management endpoint unavailable")
	}
	defer resp.Body.Close()
	response, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("application management did not complete (HTTP %d); inspect list and settle before retrying", resp.StatusCode)
	}
	_, err = stdout.Write(append(response, '\n'))
	return err
}

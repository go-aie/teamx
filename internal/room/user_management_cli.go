package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"
)

func runUserManagement(args []string, stdout, stderr io.Writer) error {
	action := "list"
	if len(args) > 0 && (args[0] == "list" || args[0] == "disable" || args[0] == "enable") {
		action = args[0]
		args = args[1:]
	}
	flags := flag.NewFlagSet("access users", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("config", "", "deployment config.toml")
	user := flags.String("user", "", "stable local user ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *path == "" || action != "list" && !validOperatorID(*user) {
		return ErrRequest
	}
	c, err := readStartupConfig(*path, true, false)
	if err != nil {
		return err
	}
	if c.Deployment.Mode != "multi-user" {
		return errors.New("user management requires a multi-user deployment")
	}
	token, err := readAdminToken(adminTokenPath(c))
	if err != nil {
		return err
	}
	listen := "127.0.0.1:8088"
	if c.Server.Listen != nil {
		listen = *c.Server.Listen
	}
	method := http.MethodGet
	var body io.Reader
	if action != "list" {
		status := "disabled"
		if action == "enable" {
			status = "active"
		}
		data, _ := json.Marshal(map[string]string{"user_id": *user, "status": status})
		body = bytes.NewReader(data)
		method = http.MethodPost
	}
	req, err := http.NewRequest(method, internalAccessEndpoint(listen)+"/v1/deployment/users", body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 40 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("local deployment management endpoint unavailable")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("user management did not complete (HTTP %d); retry or inspect local state", resp.StatusCode)
	}
	_, err = stdout.Write(append(data, '\n'))
	return err
}

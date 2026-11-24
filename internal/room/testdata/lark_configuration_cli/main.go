// This executable models the read-only native protocol used by configuration
// tests. It deliberately has no dependency on TeamX authorization or services.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type state struct {
	Profile, AppID, Brand, Version, OpenID, Tenant string
	Self                                           bool
	Gate                                           string
	SpawnChild                                     bool
	Fault                                          string
}

var base string

func main() {
	if len(os.Args) == 3 && os.Args[1] == "--fixture-child" {
		base = os.Args[2]
		publishReady("child")
		for {
			time.Sleep(time.Second)
		}
	}
	if len(os.Args) < 4 {
		os.Exit(2)
	}
	base = os.Args[1]
	args := os.Args[2:]
	record("start", args)
	code := run(args)
	record("exit", args)
	os.Exit(code)
}

func record(event string, args []string) {
	cwd, err := os.Getwd()
	must(err)
	file, err := os.OpenFile(filepath.Join(base, "native-calls.ndjson"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	must(err)
	must(json.NewEncoder(file).Encode(map[string]any{"event": event, "pid": os.Getpid(), "args": args, "cwd": cwd, "time_ns": time.Now().UnixNano()}))
	must(file.Close())
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
}
func load(name string, out any) {
	data, err := os.ReadFile(filepath.Join(base, name))
	must(err)
	must(json.Unmarshal(data, out))
}
func exists(name string) bool { _, err := os.Stat(filepath.Join(base, name)); return err == nil }
func emit(value any)          { must(json.NewEncoder(os.Stdout).Encode(value)) }
func envelope(value any)      { emit(map[string]any{"ok": true, "identity": "user", "data": value}) }

// Readers use this PID to check and stop the fixture process. Publish the
// complete value together with readiness, never an empty or partial file.
func publishReady(name string) {
	pid := strconv.Itoa(os.Getpid())
	target := filepath.Join(base, name+"-ready")
	temporary := target + "." + pid + ".tmp"
	must(os.WriteFile(temporary, []byte(pid), 0600))
	must(os.Rename(temporary, target))
}

func readState() state { var s state; load("native-fixture.json", &s); return s }

func gate(s state, name string) state {
	if s.Gate != name {
		return s
	}
	if s.SpawnChild {
		child := exec.Command(os.Args[0], "--fixture-child", base)
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		must(child.Start())
		defer child.Process.Release()
	}
	publishReady(name)
	for !exists(name + "-release") {
		time.Sleep(5 * time.Millisecond)
	}
	return readState()
}

func run(argv []string) int {
	if len(argv) < 3 || argv[0] != "--profile" || argv[1] == "" {
		return 2
	}
	a := argv[2:]
	s := readState()
	switch {
	case slices.Equal(a, []string{"--version"}):
		fmt.Println("lark-cli version " + s.Version)
	case slices.Equal(a, []string{"whoami", "--as", "user"}):
		emit(map[string]any{"profile": s.Profile, "appId": s.AppID, "brand": s.Brand, "identity": "user", "available": true, "onBehalfOf": map[string]string{"openId": s.OpenID}})
	case slices.Equal(a, []string{"config", "show"}):
		emit(map[string]string{"profile": s.Profile, "appId": s.AppID, "brand": s.Brand, "dpopMode": "required"})
	case slices.Equal(a, []string{"auth", "scopes", "--json"}):
		s = gate(s, "permission")
		var scopes []string
		load("application-scopes.json", &scopes)
		emit(map[string]any{"appId": s.AppID, "tokenType": "user", "userScopes": scopes})
	case len(a) == 5 && slices.Equal(a[:3], []string{"auth", "check", "--scope"}) && a[4] == "--json":
		var missing []string
		load("missing-scopes.json", &missing)
		granted := []string{}
		for _, scope := range strings.Fields(a[3]) {
			if !slices.Contains(missing, scope) {
				granted = append(granted, scope)
			}
		}
		emit(map[string]any{"ok": len(missing) == 0, "granted": granted, "missing": missing})
		if len(missing) > 0 {
			return 1
		}
	case slices.Equal(a, []string{"auth", "status", "--json"}):
		emit(map[string]any{"appId": s.AppID, "identities": map[string]any{"user": map[string]string{"openId": s.OpenID, "tokenStatus": "valid", "refreshExpiresAt": "2099-01-01T00:00:00Z"}}})
	case len(a) >= 9 && a[0] == "api" && slices.Equal(a[len(a)-4:], []string{"--as", "user", "--format", "json"}):
		return api(s, a)
	case len(a) >= 2 && slices.Equal(a[:2], []string{"im", "+chat-list"}):
		var data any
		load("directory.json", &data)
		envelope(data)
	default:
		fmt.Fprintln(os.Stderr, "unsupported read-only fixture command")
		return 2
	}
	return 0
}

func api(s state, a []string) int {
	method, path := a[1], a[2]
	if method != "GET" && !(method == "POST" && (path == "/open-apis/contact/v3/users/basic_batch" || path == "/open-apis/im/v1/chat_p2p/batch_query")) {
		return 2
	}
	if a[3] != "--params" {
		return 2
	}
	params := map[string]any{}
	must(json.Unmarshal([]byte(a[4]), &params))
	switch {
	case path == "/open-apis/authen/v1/user_info":
		s = gate(s, "identity")
		switch s.Fault {
		case "malformed_user_info":
			fmt.Print("{\"data\":")
			return 0
		case "failed_user_info":
			fmt.Fprintln(os.Stderr, "private-fixture-error")
			return 1
		case "stdout_limit":
			fmt.Print(strings.Repeat("x", (2<<20)+1))
			return 0
		case "stderr_limit":
			fmt.Fprint(os.Stderr, strings.Repeat("x", (64<<10)+1))
			return 1
		}
		envelope(map[string]string{"open_id": s.OpenID, "tenant_key": s.Tenant, "name": "测试用户"})
		if exists("next-open-id") {
			id, err := os.ReadFile(filepath.Join(base, "next-open-id"))
			must(err)
			s.OpenID = strings.TrimSpace(string(id))
			data, err := json.Marshal(s)
			must(err)
			must(os.WriteFile(filepath.Join(base, "native-fixture.json"), data, 0600))
		}
	case path == "/open-apis/contact/v3/users/basic_batch":
		if method != "POST" || params["user_id_type"] != "open_id" || len(params) != 1 || a[5] != "--body" {
			return 2
		}
		var body struct {
			UserIDs []string `json:"user_ids"`
		}
		must(json.Unmarshal([]byte(a[6]), &body))
		users := []any{}
		for _, id := range body.UserIDs {
			users = append(users, map[string]string{"user_id": id, "name": "测试发送者"})
		}
		envelope(map[string]any{"users": users})
	case path == "/open-apis/im/v1/chat_p2p/batch_query":
		envelope(map[string]any{"p2p_chats": []any{map[string]string{"chat_id": "oc_chat"}}})
	case strings.HasSuffix(path, "/members"):
		items := []any{map[string]string{"member_id": s.OpenID, "member_id_type": "open_id", "tenant_key": s.Tenant}}
		if !s.Self {
			items = append(items, map[string]string{"member_id": "ou_sender", "member_id_type": "open_id", "tenant_key": "external_tenant"})
		}
		envelope(map[string]any{"items": items, "has_more": false})
	case strings.HasPrefix(path, "/open-apis/im/v1/chats/"):
		tenant := "external_tenant"
		if s.Self {
			tenant = s.Tenant
		}
		mode := "group"
		if strings.HasSuffix(path, "/oc_chat") {
			mode = "p2p"
		}
		envelope(map[string]string{"chat_mode": mode, "name": "脱敏会话", "tenant_key": tenant})
	case strings.HasPrefix(path, "/open-apis/im/v1/messages/"):
		var messages []map[string]any
		load("messages.json", &messages)
		items := []any{}
		if path == "/open-apis/im/v1/messages/mget" {
			ids, ok := params["message_ids"].([]any)
			if !ok {
				return 2
			}
			for _, m := range messages {
				if slices.Contains(ids, m["message_id"]) {
					items = append(items, m)
				}
			}
		} else {
			for _, m := range messages {
				if m["message_id"] == filepath.Base(path) {
					items = append(items, m)
				}
			}
		}
		envelope(map[string]any{"items": items})
	case path == "/open-apis/im/v1/messages":
		var messages []map[string]any
		load("messages.json", &messages)
		start, e1 := strconv.ParseInt(fmt.Sprint(params["start_time"]), 10, 64)
		end, e2 := strconv.ParseInt(fmt.Sprint(params["end_time"]), 10, 64)
		if e1 != nil || e2 != nil {
			return 2
		}
		items := []any{}
		for _, m := range messages {
			created, err := strconv.ParseInt(fmt.Sprint(m["create_time"]), 10, 64)
			must(err)
			if m["chat_id"] == params["container_id"] && created/1000 >= start && created/1000 <= end {
				items = append(items, m)
			}
		}
		offset := 0
		if token, ok := params["page_token"].(string); ok {
			var err error
			offset, err = strconv.Atoi(token)
			must(err)
		}
		size, ok := params["page_size"].(float64)
		if !ok || offset < 0 || offset > len(items) || size < 1 {
			return 2
		}
		limit := min(offset+int(size), len(items))
		more := limit < len(items)
		token := ""
		if more {
			token = strconv.Itoa(limit)
		}
		envelope(map[string]any{"items": items[offset:limit], "has_more": more, "page_token": token})
	default:
		return 2
	}
	return 0
}

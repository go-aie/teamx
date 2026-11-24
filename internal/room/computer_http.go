package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

func serveComputer(w http.ResponseWriter, r *http.Request, c *computerController) {
	if c == nil {
		writeAPIError(w, ErrRequest)
		return
	}
	if r.URL.Path == "/v1/computer" && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(c.view())
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/computer/operations/") && r.Method == http.MethodGet {
		id := strings.TrimPrefix(r.URL.Path, "/v1/computer/operations/")
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, op := range c.journal.Operations {
			if op.ID == id || op.DisableID == id {
				_ = json.NewEncoder(w).Encode(c.computerOperationLocked(op))
				return
			}
		}
		http.Error(w, `{"code":"operation_not_found","message":"未找到该电脑设置操作，请读取当前配置后核对。"}`, http.StatusNotFound)
		return
	}
	if (r.URL.Path != "/v1/computer/save" && r.URL.Path != "/v1/computer/validate" && r.URL.Path != "/v1/computer/check") || r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input computerChangeInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&input) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	if input.ServiceInfo != c.service || input.InstallationID != c.owner.InstallationID || input.Caller.Kind != "human" || input.Caller.ID != c.owner.OwnerID || input.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	if r.URL.Path == "/v1/computer/check" {
		if input.ExpectedVersion != c.current.Load().Version {
			writeAPIError(w, ErrConflict)
			return
		}
		if input.RuntimeMode != "docker" || input.Provider != "aio" {
			writeAPIError(w, ErrRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		environment, err := c.docker.Check(ctx)
		if err != nil {
			writeAPIError(w, invalidField("computer", err.Error()))
			return
		}
		_ = json.NewEncoder(w).Encode(environment)
		return
	}
	if r.URL.Path == "/v1/computer/validate" {
		if computerMode(input.RuntimeMode) != "manual" {
			writeAPIError(w, ErrRequest)
			return
		}
		current := c.current.Load()
		if input.ExpectedVersion != current.Version {
			writeAPIError(w, ErrConflict)
			return
		}
		if input.Provider != "aio" {
			writeAPIError(w, ErrRequest)
			return
		}
		key := input.Credential
		if input.CredentialAction == "keep" && key == "" {
			if current.RuntimeMode == "docker" {
				writeAPIError(w, invalidField("computer", "请输入手动连接的 Runtime 管理密钥。"))
				return
			}
			var err error
			key, err = c.credentials.get(current.CredentialRef)
			if err != nil {
				writeAPIError(w, err)
				return
			}
		} else if input.CredentialAction != "replace" {
			writeAPIError(w, ErrRequest)
			return
		}
		// Validation does not persist a candidate, claim ownership or create screens.
		p, err := c.connect(input.Provider, input.Endpoint, key)
		if err == nil {
			var info computerInfoValidation
			ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
			defer cancel()
			info, err = validateComputerProvider(ctx, p, input.Provider, c.owner)
			if err == nil {
				if alias := c.manualComputerAllowed(info.InstanceID); alias != nil {
					writeAPIError(w, invalidField("computer", alias.Error()))
					return
				}
			}
			if err == nil {
				_ = json.NewEncoder(w).Encode(info)
				return
			}
		}
		// Do not relay upstream bodies or credentials from diagnostic errors.
		writeAPIError(w, invalidField("computer", "无法核验电脑连接，请检查地址、管理密钥及服务能力。"))
		return
	}
	op, err := c.save(input)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(publicComputerOperation(op))
}

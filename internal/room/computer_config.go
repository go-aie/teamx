package room

import (
	"bytes"
	"errors"
	"reflect"
	"strings"

	"github.com/go-aie/teamx/internal/computer"
	"github.com/pelletier/go-toml/v2"
)

// Connection identity is persisted separately from member/Session capabilities.
// Credential bytes never enter this document or its public view.
type computerConfig struct {
	Enabled         bool   `toml:"enabled" json:"enabled"`
	RuntimeMode     string `toml:"runtime_mode" json:"runtime_mode"`
	DockerCPUs      int    `toml:"docker_cpus" json:"docker_cpus"`
	DockerMemoryGiB int    `toml:"docker_memory_gib" json:"docker_memory_gib"`
	Provider        string `toml:"provider" json:"provider"`
	Endpoint        string `toml:"endpoint" json:"endpoint"`
	CredentialRef   string `toml:"credential_ref" json:"credential_ref"`
	MaxScreens      int    `toml:"max_screens" json:"max_screens"`
	Version         uint64 `toml:"version" json:"version"`
	InstanceID      string `toml:"instance_id" json:"instance_id"`
	ContractDigest  string `toml:"tool_contract_digest" json:"tool_contract_digest"`
	OperationID     string `toml:"operation_id" json:"operation_id"`
}

func decodeComputerTOML(source []byte, out *startupConfig) error { return toml.Unmarshal(source, out) }
func defaultComputerConfig(v computerConfig) computerConfig {
	if v.RuntimeMode == "" {
		v.RuntimeMode = "manual"
	}
	if v.DockerCPUs == 0 {
		v.DockerCPUs = 4
	}
	if v.DockerMemoryGiB == 0 {
		v.DockerMemoryGiB = 6
	}
	if v.Provider == "" {
		v.Provider = "aio"
	}
	if v.MaxScreens == 0 {
		v.MaxScreens = 2
	}
	if v.Version == 0 {
		v.Version = 1
	}
	return v
}
func (v computerConfig) validate() error {
	if v.RuntimeMode != "" && v.RuntimeMode != "manual" && v.RuntimeMode != "docker" || v.DockerCPUs < 0 || v.DockerCPUs > 64 || v.DockerMemoryGiB < 0 || v.DockerMemoryGiB > 256 || v.DockerMemoryGiB == 1 {
		return ErrInvalid
	}
	if v.Provider != "aio" || v.MaxScreens < 1 || v.MaxScreens > 256 {
		return invalidField("computer", "请选择支持的电脑服务，最大运行屏幕数为 1–256。")
	}
	if v.Endpoint != "" {
		if err := computer.ValidateEndpoint(v.Endpoint); err != nil {
			return err
		}
	}
	if v.Enabled && v.RuntimeMode != "docker" && (v.Endpoint == "" || v.CredentialRef == "") {
		return invalidField("computer", "启用电脑需要连接地址和管理密钥。")
	}
	if v.CredentialRef != "" && !validComputerCredentialRef(v.CredentialRef) {
		return ErrInvalid
	}
	return nil
}
func patchComputerConfig(source []byte, target computerConfig) ([]byte, error) {
	if err := target.validate(); err != nil {
		return nil, err
	}
	var before, after map[string]any
	if toml.Unmarshal(source, &before) != nil {
		return nil, ErrInvalid
	}
	next, err := removeConfigSubtrees(source, []string{"computer"})
	if err != nil {
		return nil, err
	}
	rendered, err := toml.Marshal(struct {
		Computer computerConfig `toml:"computer"`
	}{target})
	if err != nil {
		return nil, err
	}
	next = append(next, rendered...)
	var check startupConfig
	if len(next) > 1<<20 || toml.NewDecoder(bytes.NewReader(next)).DisallowUnknownFields().Decode(&check) != nil || check.Computer != target || toml.Unmarshal(next, &after) != nil {
		return nil, errors.New("cannot safely save Computer configuration")
	}
	delete(before, "computer")
	delete(after, "computer")
	if len(before) != 0 || len(after) != 0 {
		if !reflect.DeepEqual(before, after) {
			return nil, ErrInvalid
		}
	}
	return next, nil
}
func validComputerCredentialRef(ref string) bool {
	return strings.HasPrefix(ref, "computer-") && validRestartID(strings.TrimPrefix(ref, "computer-"))
}

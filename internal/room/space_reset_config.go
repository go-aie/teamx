package room

import (
	"errors"
	"flag"
	"io"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/pelletier/go-toml/v2"
)

// Reuse the startup conversion for every new runtime, including reset/setup
// transitions. Assembly-only dependencies remain owned by the deployment.
func mapSpaceBusinessConfig(config serviceConfig, startup startupConfig) (serviceConfig, error) {
	flags := flag.NewFlagSet("space", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.String("listen", "", "")
	merge := flags.Duration("merge-window", 100*time.Millisecond, "")
	turns := flags.Uint64("session-max-turns", defaultMaxTurns, "")
	timeout := flags.Duration("dsh-turn-timeout", dsh.DefaultTurnTimeout, "")
	days := flags.Int("inbox-expire-after-days", 3, "")
	skip := flags.Bool("inbox-skip-expired", true, "")
	if err := applyStartupSettings(flags, nil, startup); err != nil {
		return config, err
	}
	if *merge < 0 || *turns == 0 || *timeout <= 0 {
		return config, ErrRequest
	}
	expiry := notification.ExpiryPolicy{AfterDays: *days, SkipExpired: *skip}
	if err := expiry.Validate(); err != nil {
		return config, err
	}
	config.mergeWindow, config.maxTurns, config.dshTurnTimeout = *merge, *turns, *timeout
	config.inboxExpiry, config.inboxMode, config.imageMaterials = expiry, startup.Server.InboxMode, startup.Server.ImageMaterials
	return config, nil
}

// Patch the business fields only. An absent field invokes the same defaults as
// a new Home in the running version; deployment and Computer tables survive.
func spaceResetConfig(source []byte) ([]byte, error) {
	var values map[string]any
	if err := toml.Unmarshal(source, &values); err != nil {
		return nil, ErrInvalid
	}
	if values == nil {
		values = map[string]any{}
	}
	legacyKey := ""
	if runtime, ok := values["runtime"].(map[string]any); ok {
		if model, ok := runtime["model"].(map[string]any); ok {
			legacyKey, _ = model["api_key_env"].(string)
		}
		delete(runtime, "model")
	}
	values["models"] = modelCatalog{SchemaVersion: 1}
	if server, ok := values["server"].(map[string]any); ok {
		for _, key := range []string{"time_zone", "session_max_turns", "merge_window", "dsh_turn_timeout", "inbox_expire_after_days", "inbox_skip_expired", "inbox_mode", "image_materials"} {
			delete(server, key)
		}
	}
	if env, ok := values["env"].(map[string]any); ok {
		for name := range env {
			if name == legacyKey {
				delete(env, name)
				continue
			}
			switch name {
			// Explicit consumers: tool discovery, proxy/runtime deployment, Computer.
			case "PATH", "TEAMX_GITLAB_BIN", "TEAMX_GLAB_BIN", "TEAMX_LARK_BIN", "TEAMX_SCION_BIN", "TEAMX_SCN_BIN", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy", "NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "SSL_CERT_DIR", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG":
			// Fixed business overrides and historical model environment names.
			case "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "DEEPSEEK_API_KEY", "GITLAB_TOKEN", "GITLAB_ACCESS_TOKEN", "GLAB_TOKEN", "LARK_PROFILE", "FEISHU_PROFILE":
				delete(env, name)
			default:
				return nil, &requestDiagnostic{cause: ErrRequest, Message: "空间 env 含无法分类的自定义变量，请先在配置文件中整理后再重置。"}
			}
		}
	}
	return toml.Marshal(values)
}

func resetConfigDiagnostic(err error) error {
	var diagnostic *requestDiagnostic
	if errors.As(err, &diagnostic) {
		return err
	}
	return &requestDiagnostic{cause: ErrRequest, Message: "无法安全准备重置配置，请核对配置及部署模板。"}
}

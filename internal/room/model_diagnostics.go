package room

import (
	"errors"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Only local constraint codes and authored messages cross the HTTP boundary.
// Never return upstream text, submitted URLs, credentials or private paths.
func modelConfigDiagnostic(err error) error {
	var field *dsh.ModelConfigError
	if !errors.As(err, &field) {
		return invalidField("", "模型运行环境配置无效，请检查部署机上的 DSH 配置。")
	}
	message := "请检查此项模型设置。"
	switch field.Field {
	case "protocol":
		message = "请选择支持的接口协议：OpenAI 兼容或 DeepSeek Messages。"
	case "base_url":
		message = "接口地址必须是完整的 http:// 或 https:// 地址，且不能包含用户名、密码、查询参数或 # 片段。"
		if field.Reason == "required" {
			message = "OpenAI 兼容接口需要填写接口地址（Base URL）。"
		}
	case "model_id":
		message = "请填写服务商提供的模型 ID，长度不能超过 1024 字节。"
	case "reasoning_effort":
		message = "推理强度无效，请选择下拉列表中的选项或保留默认。"
		if field.Reason == "deepseek" {
			message = "DeepSeek 模型不支持 minimal、medium 或 xhigh 推理强度，请选择默认、关闭（off）、低（low）、高（high）或最高（max）。"
		}
	case "context_window":
		message = "上下文窗口必须为正整数，或留空使用默认值。"
		if field.Reason == "protocol" {
			message = "DeepSeek Messages 协议不支持自定义上下文窗口，请留空。"
		}
	case "max_tokens":
		message = "输出长度上限必须为正整数，或留空使用默认值。"
		if field.Reason == "context_window" {
			message = "输出长度上限不能超过上下文窗口，请减小输出上限或增大上下文窗口；留空的参数也会使用默认限制。"
		}
	case "input_modalities":
		message = "输入类型必须包含文本，且只能选择不重复的文本和图片。"
		if field.Reason == "protocol" {
			message = "DeepSeek Messages 协议不支持图片输入，请选择默认或文本。"
		}
	case "compat.supports_developer_role":
		message = "developer 指令兼容性仅适用于 OpenAI 兼容接口，请将此项恢复为默认。"
	}
	return invalidField(field.Field, message)
}

func setupProbeDiagnostic(probe *dsh.ProbeError) (field, message string) {
	switch probe.Code {
	case "authentication", "missing":
		return "api_key", "模型服务拒绝了身份验证，请检查 API Key 是否有效，以及是否有权访问所选模型。"
	case "dns", "connection":
		return "base_url", "无法连接模型服务，请检查接口地址和部署机的网络连接。"
	case "tls":
		return "base_url", "模型服务的 HTTPS 证书验证失败，请检查接口地址和证书配置。"
	case "route_model_access":
		return "model_id", "模型服务无法访问所选模型，请核对模型 ID、接口地址及账号权限。"
	case "rate_quota":
		return "", "模型服务限流或额度不足，请检查账号额度，稍后重试。"
	case "timeout":
		return "", "模型验证超时，请检查模型服务和网络连接后重试。"
	case "parameters_roles":
		return "", "模型服务拒绝了请求参数或指令角色，请核对接口协议和高级设置；若接口不支持 developer，请调整对应兼容性选项。"
	case "busy":
		return "", "已有模型验证正在进行，请稍后重试。"
	}
	if probe.Stage == "tool" {
		return "", "模型未通过工具调用验证，请确认所选模型和接口支持工具调用。"
	}
	return "", "模型验证失败，请检查模型服务是否支持所选接口协议和工具调用。"
}

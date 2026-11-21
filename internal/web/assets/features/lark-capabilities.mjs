// Upload readiness is independent of member grants and message schema support.
export function imageUploadStatus(capability) {
  if (capability.state === "ready") return "图片上传权限已就绪";
  if (capability.state === "unsupported") return "当前服务不支持图片上传";
  return {
    application_scopes_missing:"图片上传需管理员开通 im:resource",
    user_scopes_missing:"图片上传需当前账号授权 im:resource",
    check_missing:"图片上传权限待检查",
    check_expired:"图片上传权限检查已过期",
    account_pinning_unavailable:"图片上传需重新核验固定账号",
    credential_unavailable:"图片上传的账号凭据不可用"
  }[capability.reason_code] || "图片上传权限检查未完成，请重新检查";
}

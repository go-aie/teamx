// Shared by first use and online editing. Preserve omission versus explicit false.
export function fillModelParameters($, model = {}) {
  $("protocol").value = model.protocol || "openai-completions";
  for (const [id, key] of [["base-url","base_url"],["model-id","model_id"],["context-window","context_window"],["max-tokens","max_tokens"],["reasoning-effort","reasoning_effort"]]) $(id).value = model[key] || "";
  $("input-modalities").value = (model.input_modalities || []).join(",");
  $("developer-role").value = model.compat?.supports_developer_role === undefined ? "" : String(model.compat.supports_developer_role);
  syncModelProtocol($);
}
export function syncModelProtocol($) {
  const completions = $("protocol").value === "openai-completions";
  $("developer-compat").hidden = !completions;
  $("protocol-note").hidden = completions;
  if (!completions) $("developer-role").value = "";
}
export function readModelParameters($) {
  const model = {protocol:$("protocol").value, base_url:$("base-url").value.trim(), model_id:$("model-id").value.trim()};
  if ($("input-modalities").value) model.input_modalities = $("input-modalities").value.split(",");
  if ($("context-window").value) model.context_window = Number($("context-window").value);
  if ($("max-tokens").value) model.max_tokens = Number($("max-tokens").value);
  if ($("reasoning-effort").value) model.reasoning_effort = $("reasoning-effort").value;
  if (model.protocol === "openai-completions" && $("developer-role").value) model.compat = {supports_developer_role:$("developer-role").value === "true"};
  return model;
}

// Explicit allowlist: API keys and arbitrary form fields never enter a draft.
export const modelDraftFields = ["protocol", "base-url", "model-id", "input-modalities", "context-window", "max-tokens", "reasoning-effort", "developer-role"];
export function snapshotModelFields($) {
  const fields = Object.fromEntries(modelDraftFields.map(id => [id, $(id).value]));
  try {
    const url = new URL(fields["base-url"]);
    if (url.username || url.password || url.search || url.hash) fields["base-url"] = "";
  } catch { if (/[?#@]/.test(fields["base-url"])) fields["base-url"] = ""; }
  return fields;
}
export function restoreModelFields($, fields) {
  for (const id of modelDraftFields) if (typeof fields?.[id] === "string") $(id).value = fields[id];
  syncModelProtocol($);
}

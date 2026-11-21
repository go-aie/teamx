// Decode Markdown URLs once; file APIs receive the resulting filesystem path.
// Messages use the workspace root. Documents supply their current file path.
export function workspaceFileTarget(href, base = "", documentPath = null) {
  if (typeof href !== "string" || !href) return null;
  const hash = href.indexOf("#"), raw = hash < 0 ? href : href.slice(0,hash);
  if (raw.includes("?") || hash >= 0 && documentPath === null) return null;
  let path, fragment = "";
  try { path = decodeURIComponent(raw); if (hash >= 0) fragment = decodeURIComponent(href.slice(hash+1)); } catch { return null; }
  if (/^[\\/]/.test(path) || /[\\:\u0000-\u001f\u007f-\u009f]/.test(path) || /[\u0000-\u001f\u007f-\u009f]/.test(fragment)) return null;
  if (!path) return documentPath ? {path:documentPath,fragment} : null;
  const parts = base ? base.split("/") : [];
  for (const part of path.split("/")) {
    if (!part || part === ".") continue;
    if (part === "..") { if (!parts.length) return null; parts.pop(); }
    else parts.push(part);
  }
  return parts.length ? {path:parts.join("/"),fragment} : null;
}
export function workspaceFilePath(href, base = "") {
  return workspaceFileTarget(href,base)?.path ?? null;
}

// Used for generated heading IDs and decoded fragments, including Chinese.
export function workspaceHeadingID(text) {
  return text.normalize("NFKC").toLocaleLowerCase().trim().replace(/[^\p{L}\p{N}\p{M}_\s-]/gu, "").replace(/\s+/g,"-");
}

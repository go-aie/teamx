// Browser-only API substitute for isolated UI acceptance. No Git, credentials,
// DSH, or remote platform is accessed. Never shipped by web.go.
const key = "teamx.workspace-preview-server";
let server;
try { server = JSON.parse(sessionStorage.getItem(key)); } catch { /* Empty preview. */ }
server ||= { service_epoch: "preview-manager", operator: "preview", busy: false, rooms: [], opens: {}, workspaces: {} };
const save = () => sessionStorage.setItem(key, JSON.stringify(server));
const copy = value => structuredClone(value);
const identity = room => ({ room_id: room.room_id, room_instance: room.room_instance, service_epoch: `preview-${room.room_id}-${server.workspaces[room.room_id].workspace_version}` });
const error = code => { throw Object.assign(new Error(code), { code }); };
const team = { id: "sample", name: "UI 验收团队", description: "隔离 API 替身 · 不调用 GitLab 或模型", members: [{ id: "developer", name: "Developer" }, { id: "reviewer", name: "Reviewer" }], opening_message: "先讨论需求，再决定是否关联仓库。" };
function settleWorkspace(ws) {
  const op = ws.operation;
  if (op?.status !== "running") return;
  const elapsed = Date.now() - ws.started;
  if (elapsed < 1800) { op.phase = elapsed < 500 ? "waiting" : elapsed < 1000 ? "preparing" : elapsed < 1400 ? "committing" : "sessions"; return; }
  const room = server.rooms.find(room => room.room_id === ws.room_id);
  room.busy = false;
  if (op.target.repository?.url.includes("/fail/")) {
    op.status = "failed"; op.error_code = "preparation_failed";
  } else {
    op.status = "completed"; op.phase = "completed";
    ws.workspace = copy(op.target); ws.workspace_version++;
    ws.credential_status = ws.workspace.binding ? "configured" : "missing";
    ws.members = ws.members.map(member => ({ ...member, workspace: `${ws.workspace.root}/${member.membership_id}` }));
    room.service = identity(room);
    delete ws.observation;
  }
  save();
}
export async function fixtureRequest(path, data) {
  for (const ws of Object.values(server.workspaces)) settleWorkspace(ws);
  const url = new URL(path, location.origin);
  if (path === "/v1/ui") return { managed: true };
  if (path === "/v1/rooms") return { ...copy(server), workspaces: undefined, teams: [team] };
  if (path === "/v1/rooms/create") {
    let room = server.rooms.find(room => room.create_id === data.create_id);
    if (!room) {
      const id = crypto.randomUUID().replaceAll("-", "");
      room = { room_id: id, room_instance: `instance-${id}`, status: "ready", team_id: "sample", create_id: data.create_id,
        name: data.name || team.name, created_at: new Date().toISOString() };
      server.rooms.push(room);
      server.workspaces[id] = { room_id: id, room_instance: room.room_instance, workspace: { root: "workspaces" }, workspace_version: 1, messages: [], credential_status: "missing",
        members: ["developer", "reviewer"].map(agent_id => ({ agent_id, membership_id: `${agent_id}-${id}`, workspace: `workspaces/${agent_id}-${id}` })) };
      save();
    }
    return copy(room);
  }
  if (path === "/v1/rooms/open") {
    const room = server.rooms.find(room => room.room_id === data.room_id);
    room.service ||= identity(room); room.runtime_status = "ready";
    const receipt = { ...data, status: "completed", service: room.service };
    server.opens[data.open_id] = receipt; save(); return copy(receipt);
  }
  if (url.pathname === "/v1/rooms/open") return copy(server.opens[url.searchParams.get("open_id")]) || error("request");
  if (url.pathname === "/v1/workspace") {
    const ws = server.workspaces[url.searchParams.get("room_id")];
    if (!ws) return error("identity");
    if (url.searchParams.get("operation_id") && url.searchParams.get("operation_id") !== ws.operation?.operation_id) return error("operation_not_found");
    return { ...copy(ws), messages: undefined, started: undefined };
  }
  if (path === "/v1/workspace/apply" || path === "/v1/workspace/retry") {
    const ws = server.workspaces[data.room_id];
    if (!ws || data.room_instance !== ws.room_instance || data.service_epoch !== server.service_epoch) return error("identity");
    if (path.endsWith("retry")) {
      if (data.operation_id !== ws.operation?.operation_id) return error("conflict");
      ws.operation.status = "running"; delete ws.operation.error_code;
    } else {
      if (ws.operation?.operation_id === data.operation_id) return copy(ws.operation);
      if (ws.operation && ws.operation.status !== "completed" || ws.workspace_version !== data.expected_version) return error("conflict");
      const target = copy(data.target);
      if (target.repository && !ws.workspace.repository) target.root = `workspaces/repos/${data.operation_id}`;
      if (target.binding && data.pat) target.binding.credential_id = `preview-ref-${data.operation_id}`;
      ws.operation = { operation_id: data.operation_id, status: "running", phase: "waiting", target };
    }
    const room = server.rooms.find(room => room.room_id === ws.room_id);
    ws.observation = copy(room.service); ws.started = Date.now(); room.busy = true; delete room.service; save();
    return copy(ws.operation);
  }
  const ws = server.workspaces[data?.room_id];
  if (path === "/v1/member/list") return { members: [] };
  if (path === "/v1/read") return { messages: copy(ws.messages.filter(m => m.seq > (data.after || 0))), snapshot_seq: ws.messages.length, has_more: false };
  if (path === "/v1/send") {
    ws.messages.push({ seq: ws.messages.length + 1, text: data.text, author: { kind: "human", id: server.operator }, created_at: new Date().toISOString() }); save(); return { seq: ws.messages.length };
  }
  return error("request");
}

// Display snapshots only. Reading receipts, drafts, operations and DOM nodes
// stay with their owners; no cache entry can establish a live Room connection.
export const roomViewCacheLimits = Object.freeze({ rooms: 8, messages: 50, bytes: 4 * 1024 * 1024 });

export function createRoomViewCache({ rooms = roomViewCacheLimits.rooms, messages = roomViewCacheLimits.messages,
  bytes = roomViewCacheLimits.bytes } = {}) {
  const entries = new Map(), encoder = new TextEncoder();
  let scope, retainedBytes = 0;
  const key = info => JSON.stringify([info.room_id, info.room_instance, info.service_epoch]);
  function remove(id) {
    const entry = entries.get(id);
    if (entry) { retainedBytes -= entry.bytes; entries.delete(id); }
  }
  return {
    scope(identity) {
      const next = JSON.stringify(identity);
      if (scope !== next) { entries.clear(); retainedBytes = 0; scope = next; }
    },
    put(info, snapshot) {
      const id = key(info);
      remove(id);
      const data = JSON.stringify({ ...snapshot, messages: snapshot.messages.slice(-messages) });
      const size = encoder.encode(data).byteLength;
      if (size > bytes || rooms < 1) return;
      entries.set(id, { info: { ...info }, data, bytes: size, clearedThrough: snapshot.clearedThrough }); retainedBytes += size;
      while (entries.size > rooms || retainedBytes > bytes) remove(entries.keys().next().value);
    },
    get(info, clearedThrough = 0) {
      const id = key(info), entry = entries.get(id);
      if (!entry) return;
      if (entry.clearedThrough < clearedThrough) { remove(id); return; }
      entries.delete(id); entries.set(id, entry);
      return JSON.parse(entry.data);
    },
    forget(room) {
      for (const [id, entry] of entries) if (entry.info.room_id === room.room_id && entry.info.room_instance === room.room_instance) remove(id);
    },
    reconcile(catalog) {
      for (const [id, entry] of entries) {
        const room = catalog.find(room => room.room_id === entry.info.room_id && room.room_instance === entry.info.room_instance);
        if (!room || room.status !== "ready" || room.service?.service_epoch !== entry.info.service_epoch ||
          (room.human_read?.cleared_through || 0) > entry.clearedThrough) remove(id);
      }
    },
    get size() { return entries.size; },
    get bytes() { return retainedBytes; },
  };
}

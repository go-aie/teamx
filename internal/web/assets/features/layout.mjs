// Room/member sidebars, responsive visibility and width persistence.
export function createLayout({ env, roomsUI, runtime, notificationsUI, membersUI, settingsUI }) {
  const { document, window } = env;
  const { state, $, storage } = runtime;

  let directMobileDetails = false, directMobileRooms = false, directNotificationsOpen = false;

  function roomsPanelOpen() { return roomsUI.isDirect() && window.innerWidth <= 640 ? directMobileRooms : panels.rooms; }

  function membersPanelOpen() { return roomsUI.isDirect() && window.innerWidth <= 640 ? directMobileDetails : panels.members; }

  const panelsKey = "teamx.room-panels";

  const panels = { rooms: true, members: true, roomWidth: null, memberWidth: null };

  let sidebarResize = null;

  function savePanels() {
    try { storage?.setItem(panelsKey, JSON.stringify(panels)); } catch { /* Keep this page's selection in memory. */ }
  }

  const sidebarConfig = {
    rooms: { rail: "room-rail", handle: "room-resizer", key: "roomWidth", min: 200, max: 360 },
    members: { rail: "member-rail", handle: "member-resizer", key: "memberWidth", min: 220, max: 720 },
  };

  function sidebarBudget() {
    const rooms = state.managed ? (panels.rooms ? 200 : 52) : 0;
    const members = panels.members ? 220 : 0;
    const total = $("workspace").clientWidth;
    // Preserve 480px for messages where space permits; both sidebars retain their minimums on small desktops.
    return total - Math.min(480, Math.max(0, total - rooms - members));
  }

  function sidebarWidthLimits(side) {
    const config = sidebarConfig[side], other = side === "rooms" ? "members" : "rooms";
    const otherWidth = other === "rooms" && !state.managed ? 0 : !panels[other] ? (other === "rooms" ? 52 : 0)
      : $(sidebarConfig[other].rail).getBoundingClientRect().width;
    return { min: config.min, max: Math.max(config.min, Math.min(config.max, sidebarBudget() - otherWidth)) };
  }

  function applySidebarWidths(priority = "rooms") {
    if (window.innerWidth <= 640) return;
    const workspace = $("workspace"), styles = window.getComputedStyle(workspace);
    const widths = {};
    for (const [side, config] of Object.entries(sidebarConfig)) {
      const prefix = side === "rooms" ? "rooms" : "members";
      const preferred = panels[config.key] ?? Number.parseFloat(styles.getPropertyValue(`--${prefix}-default-width`));
      widths[side] = side === "rooms" && !state.managed ? 0 : !panels[side] ? (side === "rooms" ? 52 : 0)
        : Math.max(config.min, Math.min(config.max, preferred));
    }
    const budget = sidebarBudget();
    // Clamp displayed widths without overwriting the user's saved preference on a narrower window.
    for (const side of [priority === "rooms" ? "members" : "rooms", priority]) {
      if (!panels[side] || side === "rooms" && !state.managed) continue;
      const other = side === "rooms" ? "members" : "rooms";
      widths[side] = Math.max(sidebarConfig[side].min, Math.min(widths[side], budget - widths[other]));
    }
    for (const [side, config] of Object.entries(sidebarConfig)) {
      if (!panels[side] || side === "rooms" && !state.managed) continue;
      workspace.style.setProperty(`--${side === "rooms" ? "rooms" : "members"}-open-width`, `${widths[side]}px`);
    }
    for (const [side, config] of Object.entries(sidebarConfig)) {
      const limits = sidebarWidthLimits(side), handle = $(config.handle);
      handle.setAttribute("aria-valuemin", String(limits.min)); handle.setAttribute("aria-valuemax", String(limits.max));
      handle.setAttribute("aria-valuenow", String(Math.round(widths[side])));
    }
  }

  function setSidebarWidth(side, width) {
    const limits = sidebarWidthLimits(side);
    panels[sidebarConfig[side].key] = Math.round(Math.max(limits.min, Math.min(limits.max, width)));
    applySidebarWidths(side);
  }

  function renderPanels() {
    const workspace = document.querySelector(".workspace");
    const membersOpen = membersPanelOpen(), roomsOpen = roomsPanelOpen();
    workspace.classList.toggle("direct-conversation", roomsUI.isDirect());
    workspace.classList.toggle("direct-notifications-open", roomsUI.isDirect() && directNotificationsOpen);
    workspace.classList.toggle("rooms-collapsed", !roomsOpen);
    workspace.classList.toggle("members-collapsed", !membersOpen);
    $("room-rail").hidden = !state.managed;
    $("room-nav").hidden = !state.managed;
    $("room-brand").inert = !roomsOpen;
    $("room-brand").setAttribute("aria-hidden", String(!roomsOpen));
    for (const [side, id] of [["rooms", "room-nav"], ["members", "member-sidebar"]]) {
      // Keep content mounted for the slide, while removing collapsed controls from keyboard/AT access.
      const open = side === "members" ? membersOpen : roomsOpen;
      $(id).inert = !open;
      $(id).setAttribute("aria-hidden", String(!open));
    }
    $("toggle-rooms").hidden = !state.managed;
    $("member-rail").inert = !membersOpen;
    $("member-rail").setAttribute("aria-hidden", String(!membersOpen));
    if (!membersOpen && $("activity-dialog").open) $("activity-dialog").close();
    if (!membersOpen && $("notification-detail").open) $("notification-detail").close();
    notificationsUI.renderContextViews();
    applySidebarWidths();
    for (const [side, name] of [["rooms", "会话侧栏"], ["members", roomsUI.isDirect() ? "助理详情" : "团队成员"]]) {
      const button = $("toggle-" + side);
      const open = side === "members" ? membersOpen : roomsOpen;
      const label = `${open ? "收起" : "展开"}${name}`;
      button.setAttribute("aria-expanded", String(open));
      button.setAttribute("aria-label", label);
      button.setAttribute("title", label);
    }
  }

  function resetDirectNavigation() {
    directMobileDetails = false; directMobileRooms = false; directNotificationsOpen = false;
  }

  function expandMembers() {
    panels.members = true;
    if (roomsUI.isDirect() && window.innerWidth <= 640) directMobileDetails = true;
  }

  function collapseMembers() {
    if (window.innerWidth <= 640) directMobileDetails = false;
    else panels.members = false;
  }

  function showConversationNotifications() { directNotificationsOpen = true; }
  function hideConversationNotifications() { directNotificationsOpen = false; }

  function showNotificationDetail() {
    panels.members = true; savePanels(); renderPanels();
  }

  function mount() {
    try {
      const saved = JSON.parse(storage?.getItem(panelsKey));
      for (const side of ["rooms", "members"]) if (typeof saved?.[side] === "boolean") panels[side] = saved[side];
      for (const key of ["roomWidth", "memberWidth"]) if (Number.isFinite(saved?.[key]) && saved[key] > 0) panels[key] = saved[key];
    } catch { /* Sidebars remain usable when tab storage is unavailable. */ }

    for (const side of ["rooms", "members"]) $("toggle-" + side).addEventListener("click", () => {
      if (side === "members" && roomsUI.isDirect() && window.innerWidth <= 640) directMobileDetails = !directMobileDetails;
      else if (side === "rooms" && roomsUI.isDirect() && window.innerWidth <= 640) directMobileRooms = !directMobileRooms;
      else panels[side] = !panels[side];
      savePanels();
      renderPanels();
      membersUI.syncDirectDetail();
    });

    for (const [side, config] of Object.entries(sidebarConfig)) {
      const handle = $(config.handle), direction = side === "rooms" ? 1 : -1;
      const enabled = () => panels[side] && (side !== "rooms" || state.managed) && window.innerWidth > 640;
      handle.addEventListener("pointerdown", event => {
        if (event.button !== 0 || event.isPrimary === false || !enabled() || sidebarResize) return;
        event.preventDefault();
        sidebarResize = { side, pointerId: event.pointerId, x: event.clientX, width: $(config.rail).getBoundingClientRect().width };
        handle.setPointerCapture(event.pointerId);
        $("workspace").classList.add(`resizing-${side}`);
      });
      handle.addEventListener("pointermove", event => {
        if (sidebarResize?.side !== side || sidebarResize.pointerId !== event.pointerId) return;
        setSidebarWidth(side, sidebarResize.width + direction * (event.clientX - sidebarResize.x));
      });
      const finish = event => {
        if (sidebarResize?.side !== side || sidebarResize.pointerId !== event.pointerId) return;
        sidebarResize = null;
        $("workspace").classList.remove(`resizing-${side}`);
        if (handle.hasPointerCapture(event.pointerId)) handle.releasePointerCapture(event.pointerId);
        savePanels();
      };
      for (const type of ["pointerup", "pointercancel", "lostpointercapture"]) handle.addEventListener(type, finish);
      handle.addEventListener("keydown", event => {
        if (!enabled()) return;
        const limits = sidebarWidthLimits(side), width = $(config.rail).getBoundingClientRect().width;
        const next = { ArrowLeft: width - direction * 20, ArrowRight: width + direction * 20, Home: limits.min, End: limits.max }[event.key];
        if (next === undefined) return;
        event.preventDefault(); setSidebarWidth(side, next); savePanels();
      });
    }

    window.addEventListener("resize", () => { renderPanels(); membersUI.syncDirectDetail(); settingsUI.renderSettingsNavigation(); });

    renderPanels();
  }

  return {
    mount,
    resetDirectNavigation,
    expandMembers,
    collapseMembers,
    showConversationNotifications,
    hideConversationNotifications,
    showNotificationDetail,
    get membersExpanded() { return panels.members; },
    get directNotificationsOpen() { return directNotificationsOpen; },
    membersPanelOpen,
    savePanels,
    renderPanels,
  };
}

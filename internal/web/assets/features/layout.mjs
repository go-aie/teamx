import { compactViewport } from "../client.mjs";
// Room/member sidebars, responsive visibility and width persistence.
export function createLayout({ env, roomsUI, runtime, notificationsUI, membersUI, settingsUI, userMenuUI }) {
  const { document, window } = env;
  const { state, $, storage } = runtime;

  let directMobileDetails = false, directMobileRooms = false;
  let mobileTab = "messages";
  const mobileScroll = { messages:0, profile:0 };

  function roomsPanelOpen() { return compactViewport(window) ? directMobileRooms || !!(state.managed && state.catalog && !roomsUI.selectedEntry()) : panels.rooms; }

  function membersPanelOpen() { return compactViewport(window) ? directMobileDetails : panels.members; }

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
    if (compactViewport(window)) return;
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
    const mobile = compactViewport(window);
    const membersOpen = membersPanelOpen(), roomsOpen = roomsPanelOpen();
    const mobileHome = mobile && state.managed && roomsOpen && !membersOpen;
    const profileOpen = mobileHome && mobileTab === "profile";
    workspace.classList.toggle("mobile-profile-open", profileOpen);
    workspace.classList.toggle("direct-conversation", roomsUI.isDirect());
    workspace.classList.toggle("rooms-collapsed", !roomsOpen);
    workspace.classList.toggle("members-collapsed", !membersOpen);
    $("room-rail").hidden = !state.managed;
    $("room-rail").inert = mobile && (!roomsOpen || membersOpen);
    $("room-nav").hidden = !state.managed || profileOpen;
    $("mobile-profile").hidden = !profileOpen;
    $("mobile-primary-navigation").hidden = !mobileHome;
    for (const tab of ["messages", "profile"]) {
      const button = $(`mobile-tab-${tab}`);
      if (mobileTab === tab) button.setAttribute("aria-current", "page");
      else button.removeAttribute("aria-current");
    }
    for (const tab of ["general", "models", "platforms", "security"]) {
      $(`profile-${tab}`).disabled = !state.catalogConnected;
    }
    $("room-brand").inert = !roomsOpen && !compactViewport(window);
    $("room-brand").setAttribute("aria-hidden", String(!roomsOpen && !compactViewport(window)));
    for (const [side, id] of [["rooms", "room-nav"], ["members", "member-sidebar"]]) {
      // Keep content mounted for the slide, while removing collapsed controls from keyboard/AT access.
      const open = side === "members" ? membersOpen : roomsOpen && !profileOpen;
      $(id).inert = !open;
      $(id).setAttribute("aria-hidden", String(!open));
    }
    $("toggle-rooms").hidden = !state.managed || mobile;
    $("mobile-back-to-rooms").hidden = !state.managed || !mobile;
    const filterParent = $(mobile ? "mobile-home-actions" : "conversation-filter-container");
    const createParent = $(mobile ? "mobile-home-actions" : "room-list-actions");
    if ($("conversation-filter").parentElement !== filterParent) {
      roomsUI.closeFilterMenu(); userMenuUI.close();
      filterParent.append($("conversation-filter"));
      createParent.append($("new-room"));
    }
    roomsUI.renderConversationFilter();
    if ($("user-menu-container").parentElement !== $("room-rail")) {
      $("room-rail").insertBefore($("user-menu-container"), $("mobile-primary-navigation"));
    }
    $("room-menu-label").disabled = !state.info;
    const settingsParent = $(roomsUI.isDirect() ? state.managed ? "platforms-member-view" : "activity-body" : "member-sidebar");
    if ($("conversation-settings").parentElement !== settingsParent) settingsParent.append($("conversation-settings"));
    $("member-rail").inert = !membersOpen;
    $("member-rail").setAttribute("aria-hidden", String(!membersOpen));
    if (!membersOpen && $("activity-dialog").open) $("activity-dialog").close();
    if (!membersOpen && $("notification-detail").open) $("notification-detail").close();
    if (compactViewport(window)) {
      $("conversation").inert = roomsOpen || membersOpen;
    } else $("conversation").inert = false;
    notificationsUI.renderContextViews();
    applySidebarWidths();
    runtime.navigationChanged();
    runtime.conversationVisibilityChanged?.();
    for (const [side, name] of [["rooms", "会话侧栏"], ["members", roomsUI.isDirect() ? "助理详情" : "房间详情"]]) {
      const button = $(side === "members" ? "room-menu-label" : "toggle-rooms");
      const open = side === "members" ? membersOpen : roomsOpen;
      const label = side === "members" ? name : `${open ? "收起" : "展开"}${name}`;
      button.setAttribute("aria-expanded", String(open));
      button.setAttribute("aria-label", label);
      button.setAttribute("title", label);
    }
  }

  function resetDirectNavigation() {
    directMobileDetails = false; directMobileRooms = false;
    mobileTab = "messages";
  }

  function expandMembers() {
    panels.members = true;
    if (compactViewport(window)) { directMobileDetails = true; directMobileRooms = false; }
  }

  function collapseMembers() {
    if (compactViewport(window)) directMobileDetails = false;
    else panels.members = false;
  }


  function captureMemberPanel() {
    const saved = { members: panels.members, mobile: directMobileDetails, rooms: directMobileRooms };
    return () => {
      panels.members = saved.members; directMobileDetails = saved.mobile;
      directMobileRooms = saved.rooms;
      savePanels(); renderPanels();
    };
  }

  function showNotificationDetail() {
    expandMembers(); savePanels(); renderPanels();
  }

  function closeMobilePanels() {
    const rooms = directMobileRooms;
    directMobileDetails = false; directMobileRooms = false;
    renderPanels(); membersUI.syncDirectDetail(); $(rooms ? "mobile-back-to-rooms" : "room-menu-label").focus();
  }

  function showMobileRooms() {
    if (!compactViewport(window) || !state.managed) return;
    const fromProfile = mobileTab === "profile";
    if (fromProfile) mobileScroll.profile = $("mobile-profile").scrollTop;
    mobileTab = "messages";
    roomsUI.closeFilterMenu();
    directMobileRooms = true; directMobileDetails = false;
    renderPanels();
    if (fromProfile) $("room-nav").scrollTop = mobileScroll.messages;
    $("room-nav").focus({preventScroll:true});
  }

  function showMobileProfile() {
    if (!compactViewport(window) || !state.managed) return;
    if (mobileTab !== "profile") mobileScroll.messages = $("room-nav").scrollTop;
    roomsUI.closeFilterMenu(); userMenuUI.close();
    mobileTab = "profile";
    directMobileRooms = true; directMobileDetails = false;
    renderPanels();
    $("mobile-profile").scrollTop = mobileScroll.profile;
    $("mobile-profile").focus({preventScroll:true});
  }

  function updateViewport() {
    const viewport = window.visualViewport;
    const height = viewport?.scale === 1 ? viewport.height : window.innerHeight;
    if (height) document.documentElement?.style.setProperty("--app-height", `${Math.round(height)}px`);
    document.documentElement?.style.setProperty("--app-top", `${viewport?.scale === 1 ? Math.round(viewport.offsetTop) : 0}px`);
    if (height) $("workspace").style.setProperty("--app-height", `${Math.round(height)}px`);
    $("workspace").style.setProperty("--app-top", `${viewport?.scale === 1 ? Math.round(viewport.offsetTop) : 0}px`);
  }

  function mount() {
    try {
      const saved = JSON.parse(storage?.getItem(panelsKey));
      for (const side of ["rooms", "members"]) if (typeof saved?.[side] === "boolean") panels[side] = saved[side];
      for (const key of ["roomWidth", "memberWidth"]) if (Number.isFinite(saved?.[key]) && saved[key] > 0) panels[key] = saved[key];
    } catch { /* Sidebars remain usable when tab storage is unavailable. */ }

    for (const side of ["rooms", "members"]) {
      const toggle = () => {
        if (side === "members" && compactViewport(window)) directMobileDetails = !directMobileDetails;
        else if (side === "rooms" && compactViewport(window)) directMobileRooms = !directMobileRooms;
        else panels[side] = !panels[side];
        if (compactViewport(window)) {
          if (side === "rooms" && directMobileRooms) directMobileDetails = false;
          if (side === "members" && directMobileDetails) directMobileRooms = false;
        }
        savePanels();
        renderPanels();
        membersUI.syncDirectDetail();
      };
      $(side === "members" ? "room-menu-label" : "toggle-rooms").addEventListener("click", () => {
        toggle();
        if (side !== "members") return;
        if (!membersPanelOpen()) $("room-menu-label").focus();
        else $(roomsUI.isDirect() ? "close-activity" : "member-list-title").focus();
      });
    }

    for (const [side, config] of Object.entries(sidebarConfig)) {
      const handle = $(config.handle), direction = side === "rooms" ? 1 : -1;
      const enabled = () => panels[side] && (side !== "rooms" || state.managed) && !compactViewport(window);
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

    $("close-mobile-members").addEventListener("click", closeMobilePanels);
    $("close-room-details").addEventListener("click", () => {
      collapseMembers(); savePanels(); renderPanels(); $("room-menu-label").focus();
    });
    $("member-list-view").addEventListener("keydown", event => {
      if (event.key !== "Escape" || event.defaultPrevented || contextDetailOpen()) return;
      event.preventDefault(); collapseMembers(); savePanels(); renderPanels(); $("room-menu-label").focus();
    });
    $("mobile-back-to-rooms").addEventListener("click", showMobileRooms);
    $("mobile-tab-messages").addEventListener("click", showMobileRooms);
    $("mobile-tab-profile").addEventListener("click", showMobileProfile);
    for (const tab of ["general", "models", "platforms", "security"]) {
      const button = $(`profile-${tab}`);
      button.addEventListener("click", () => settingsUI.openSettings(tab, button));
    }
    window.visualViewport?.addEventListener("resize", updateViewport);
    window.visualViewport?.addEventListener("scroll", updateViewport);
    window.addEventListener("resize", () => { updateViewport(); renderPanels(); membersUI.syncDirectDetail(); settingsUI.renderSettingsNavigation(); });

    updateViewport(); renderPanels();
  }

  function contextDetailOpen() {
    return $("activity-dialog").open || $("notification-detail").open || document.querySelector("dialog:modal");
  }

  return {
    mount,
    roomsPanelOpen,
    showMobileRooms,
    get mobileProfileOpen() { return compactViewport(window) && state.managed && roomsPanelOpen() && mobileTab === "profile"; },
    closeMobilePanels,
    resetDirectNavigation,
    expandMembers,
    collapseMembers,
    captureMemberPanel,
    showNotificationDetail,
    get membersExpanded() { return panels.members; },
    membersPanelOpen,
    conversationVisible: () => !$("conversation").inert && !$("conversation").hidden,
    savePanels,
    renderPanels,
  };
}

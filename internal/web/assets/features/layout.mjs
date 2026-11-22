import { compactViewport } from "../client.mjs";
// Room/member sidebars, responsive visibility and width persistence.
export function createLayout({ env, roomsUI, runtime, notificationsUI, membersUI, settingsUI, userMenuUI, filesUI, calendarUI, conversationToolsUI }) {
  const { document, window } = env;
  const { state, $, storage } = runtime;

  let directMobileDetails = false, directMobileRooms = false;
  let mobileTab = "messages";
  const mobileScroll = { messages:0, profile:0 };
  let auxiliary = null, auxiliaryExpanded = false, hiddenViews = [];

  function roomsPanelOpen() { return compactViewport(window) ? directMobileRooms || !!(state.managed && state.catalog && !roomsUI.selectedEntry()) : panels.rooms; }

  function membersPanelOpen() { return !auxiliary && (compactViewport(window) ? directMobileDetails : panels.members); }

  // Content features retain their own view state; layout owns the shared rail.
  function hideAuxiliary() {
    if (auxiliary === "files") filesUI.hide();
    if (auxiliary === "calendar") calendarUI.hide();
  }

  function openAuxiliary(kind) {
    if (auxiliary === kind) return;
    if (!auxiliary) {
      hiddenViews = ["activity-dialog", "notification-detail"].map(id => [$(id), $(id).hidden]);
      for (const [element] of hiddenViews) element.hidden = true;
    } else hideAuxiliary();
    auxiliary = kind; auxiliaryExpanded = false;
    conversationToolsUI.close(); renderPanels();
  }

  function closeAuxiliary(kind) {
    if (auxiliary !== kind) return;
    auxiliary = null; auxiliaryExpanded = false;
    for (const [element, hidden] of hiddenViews) element.hidden = hidden;
    hiddenViews = []; renderPanels();
  }

  function showMemberContent() {
    const kind = auxiliary;
    if (!kind) return;
    hideAuxiliary(); closeAuxiliary(kind);
  }

  function toggleAuxiliaryExpanded() {
    if (!auxiliary) return;
    auxiliaryExpanded = !auxiliaryExpanded; renderPanels();
  }

  const panelsKey = "teamx.room-panels";

  const panels = { rooms: true, members: true, roomWidth: null, memberWidth: null, fileWidth: null, calendarWidth: null };

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
    if (side === "members" && auxiliary) {
      const available = $("workspace").clientWidth - (state.managed ? $("room-rail").getBoundingClientRect().width : 0);
      return { min: 400, max: Math.max(400, available - 480) };
    }
    const config = sidebarConfig[side], other = side === "rooms" ? "members" : "rooms";
    const otherWidth = other === "rooms" && !state.managed ? 0 : !panels[other] ? (other === "rooms" ? 52 : 0)
      : $(sidebarConfig[other].rail).getBoundingClientRect().width;
    return { min: config.min, max: Math.max(config.min, Math.min(config.max, sidebarBudget() - otherWidth)) };
  }

  function applySidebarWidths(priority = "rooms") {
    if (compactViewport(window)) return;
    if (auxiliary) {
      const limits = sidebarWidthLimits("members"), handle = $("member-resizer");
      const available = $("workspace").clientWidth - (state.managed ? $("room-rail").getBoundingClientRect().width : 0);
      const key = auxiliary === "files" ? "fileWidth" : "calendarWidth";
      const width = Math.max(limits.min, Math.min(limits.max, panels[key] ?? Math.max(600, available * .5)));
      $("workspace").style.setProperty("--members-open-width", `${width}px`);
      handle.setAttribute("aria-valuemin",String(limits.min)); handle.setAttribute("aria-valuemax",String(limits.max)); handle.setAttribute("aria-valuenow",String(Math.round(width)));
      return;
    }
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
    panels[side === "members" && auxiliary ? auxiliary === "files" ? "fileWidth" : "calendarWidth" : sidebarConfig[side].key] = Math.round(Math.max(limits.min, Math.min(limits.max, width)));
    applySidebarWidths(side);
  }

  function renderPanels() {
    const workspace = document.querySelector(".workspace");
    const mobile = compactViewport(window);
    const membersOpen = membersPanelOpen() || !!auxiliary, roomsOpen = roomsPanelOpen();
    const available = $("workspace").clientWidth - (state.managed ? (roomsOpen ? $("room-rail").getBoundingClientRect().width : 52) : 0);
    const auxiliaryCover = !!auxiliary && (mobile || auxiliaryExpanded || available < 1080);
    workspace.classList.toggle("files-open", auxiliary === "files");
    workspace.classList.toggle("calendar-open", auxiliary === "calendar");
    workspace.classList.toggle("auxiliary-open", !!auxiliary);
    workspace.classList.toggle("auxiliary-cover", auxiliaryCover);
    workspace.classList.toggle("auxiliary-expanded", !!auxiliary && auxiliaryExpanded);
    for (const kind of ["files", "calendar"]) {
      const expand = $(`${kind}-expand`), label = auxiliaryExpanded ? "还原" : "展开";
      expand.title = label; expand.setAttribute("aria-label", label);
      expand.setAttribute("aria-expanded", String(auxiliaryExpanded));
      if (kind === "files") $("files-expand-label").textContent = label;
      $(`${kind}-expand-icon`).setAttribute("d", auxiliaryExpanded ? "M20 10h-6V4m0 6 6-6M4 14h6v6m0-6-6 6" : "M14 4h6v6M20 4l-6 6M10 20H4v-6M4 20l6-6");
    }
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
    const settingsParent = $(roomsUI.isDirect() ? "agent-controls" : "member-sidebar");
    if ($("conversation-settings").parentElement !== settingsParent) settingsParent.append($("conversation-settings"));
    $("member-rail").inert = !membersOpen;
    $("member-rail").setAttribute("aria-hidden", String(!membersOpen));
    if (!membersOpen && $("activity-dialog").open) $("activity-dialog").close();
    if (!membersOpen && $("notification-detail").open) $("notification-detail").close();
    if (auxiliaryCover) $("conversation").inert = true;
    else if (compactViewport(window)) {
      $("conversation").inert = roomsOpen || membersOpen;
    } else $("conversation").inert = false;
    notificationsUI.renderContextViews();
    applySidebarWidths();
    runtime.navigationChanged();
    runtime.conversationVisibilityChanged?.();
    const toggle = $("toggle-rooms"), label = `${roomsOpen ? "收起" : "展开"}会话侧栏`;
    toggle.setAttribute("aria-expanded", String(roomsOpen));
    toggle.setAttribute("aria-label", label); toggle.title = label;
    runtime.conversationToolsChanged?.();
  }

  function resetDirectNavigation() {
    showMemberContent();
    filesUI.close({focus:false}); conversationToolsUI.close();
    directMobileDetails = false; directMobileRooms = false;
    mobileTab = "messages";
  }

  function expandMembers() {
    showMemberContent();
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
    renderPanels(); $(rooms ? "mobile-back-to-rooms" : "room-menu-label").focus();
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

  function toggleDetails() {
    const fromAuxiliary = !!auxiliary;
    showMemberContent();
    if (compactViewport(window)) {
      directMobileDetails = fromAuxiliary || !directMobileDetails;
      if (directMobileDetails) directMobileRooms = false;
    } else panels.members = fromAuxiliary || !panels.members;
    savePanels(); renderPanels(); membersUI.syncDirectDetail();
    if (membersPanelOpen()) $(roomsUI.isDirect() ? "close-activity" : "member-list-title").focus();
    else ($("conversation-details").hidden ? $("room-menu-label") : $("conversation-details")).focus();
  }

  function mount() {
    try {
      const saved = JSON.parse(storage?.getItem(panelsKey));
      for (const side of ["rooms", "members"]) if (typeof saved?.[side] === "boolean") panels[side] = saved[side];
      for (const key of ["roomWidth", "memberWidth", "fileWidth", "calendarWidth"]) if (Number.isFinite(saved?.[key]) && saved[key] > 0) panels[key] = saved[key];
    } catch { /* Sidebars remain usable when tab storage is unavailable. */ }

    $("toggle-rooms").addEventListener("click", () => {
      if (compactViewport(window)) directMobileRooms = !directMobileRooms;
      else panels.rooms = !panels.rooms;
      if (compactViewport(window) && directMobileRooms) directMobileDetails = false;
      savePanels(); renderPanels();
    });

    for (const [side, config] of Object.entries(sidebarConfig)) {
      const handle = $(config.handle), direction = side === "rooms" ? 1 : -1;
      const enabled = () => (panels[side] || side === "members" && auxiliary) && !$("workspace").classList.contains("auxiliary-cover") && (side !== "rooms" || state.managed) && !compactViewport(window);
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
    window.addEventListener("resize", () => { updateViewport(); renderPanels(); settingsUI.renderSettingsNavigation(); });

    updateViewport(); renderPanels();
  }

  function contextDetailOpen() {
    return $("activity-dialog").open || $("notification-detail").open || document.querySelector("dialog:modal");
  }

  return {
    mount,
    showMemberContent,
    toggleDetails,
    openAuxiliary,
    closeAuxiliary,
    toggleAuxiliaryExpanded,
    get auxiliaryOpen() { return !!auxiliary; },
    get auxiliaryExpanded() { return auxiliaryExpanded; },
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

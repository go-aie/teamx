import { createModels } from "./models.mjs";
import { createAccessSettings } from "./access-settings.mjs";
import { compactViewport } from "../client.mjs";
import { createSystemTime, createTimeInput } from "../time.mjs";

// Service time zone, settings navigation and dialog lifecycle.
export function createSettings({ env, runtime, connectionsUI, conversationUI, roomsUI, workspaceUI, policyUI, notificationsUI, onboardingUI, membersUI }) {
  const { document, window, request } = env;
  const { state, $, notice, dismissOnBackdrop } = runtime;

  const models = createModels({env, runtime, openSettings:() => openSettings("models")});
  const access = createAccessSettings({env, runtime});
  const settingsTabs = ["general", "models", "platforms", "security"];
  const availableSettingsTabs = () => state.managed ? settingsTabs : ["general", "security"];
  const systemTime = createSystemTime();

  const timeInputs = new Map(["lark-backfill-start", "gitlab-backfill-start", "onboarding-backfill-start", "policy-expires"].map(id => [id, createTimeInput(systemTime)]));

  const timezoneForm = {dirty:false, saving:false, message:""};

  let settingsTab = "general";

  const timezonePicker = {open:false, query:"", active:-1, candidates:[], shown:[]};

  let browserTimeZone = "";

  function formatDate(value, options) { return systemTime.format(value, options); }

  function bindSystemTime(element, render) {
    element.dataset.systemTime = ""; element.refreshSystemTime = render; render();
  }

  function fillTimeInput(id, value) { $(id).value = timeInputs.get(id).fill(value); }

  function readTimeInput(id) {
    if ($(id).validity?.badInput) throw new Error("请输入完整有效的时间；若系统时区已变更，请重新确认或修改。");
    return timeInputs.get(id).read($(id).value);
  }

  function timeZoneHasChanges() { return $("system-timezone").value.trim() !== systemTime.zone; }

  function canSaveTimeZone() {
    return !timezoneForm.saving && !!systemTime.epoch && !!$("system-timezone").value.trim() && timeZoneHasChanges();
  }

  function isTimeZoneDraftHint() { return ["有未保存的更改", "已填入浏览器时区，保存后生效。"].includes(timezoneForm.message); }

  function renderTimeZoneFeedback() {
    const draftHint = isTimeZoneDraftHint();
    $("timezone-draft-hint").hidden = !draftHint;
    $("timezone-draft-hint").textContent = draftHint ? "有未保存的更改" : "";
    $("timezone-feedback").textContent = draftHint ? "" : timezoneForm.message;
  }

  function renderTimeZone() {
    $("timezone-current").textContent = systemTime.error || `当前生效：${systemTime.zone}`;
    $("timezone-current").dataset.kind = systemTime.error ? "error" : "";
    if (!timezoneForm.dirty) $("system-timezone").value = systemTime.zone || "";
    $("system-timezone").disabled = timezoneForm.saving;
    $("timezone-picker-toggle").disabled = timezoneForm.saving;
    $("timezone-save").disabled = !canSaveTimeZone();
    $("timezone-save").textContent = timezoneForm.saving ? "保存中…" : "保存时区";
    $("timezone-browser").hidden = !browserTimeZone || browserTimeZone === systemTime.zone;
    $("timezone-browser").disabled = timezoneForm.saving;
    $("timezone-browser").textContent = `使用浏览器时区（${browserTimeZone}）`;
    if (!timeZoneHasChanges() && isTimeZoneDraftHint()) timezoneForm.message = "";
    renderTimeZoneFeedback();
    let supported = [];
    try { supported = Intl.supportedValuesOf("timeZone"); } catch { /* The server validates manually entered names too. */ }
    timezonePicker.candidates = [...new Set(["Asia/Shanghai", "UTC", systemTime.zone, browserTimeZone, ...supported])].filter(Boolean);
    if (timezoneForm.saving) timezonePicker.open = false;
    renderTimeZoneOptions(); renderSettingsNavigation();
    notice("timezone-error", systemTime.error);
  }

  function renderTimeZoneOptions() {
    const picker = timezonePicker, query = picker.query.trim().toLowerCase();
    picker.shown = picker.candidates.filter(zone => zone.toLowerCase().includes(query));
    if (picker.active >= picker.shown.length) picker.active = -1;
    $("timezone-options").hidden = !picker.open;
    for (const id of ["system-timezone", "timezone-picker-toggle"]) $(id).setAttribute("aria-expanded", String(picker.open));
    $("system-timezone").setAttribute("aria-activedescendant", picker.open && picker.active >= 0 ? `timezone-option-${picker.active}` : "");
    if (!picker.open) return;
    const nodes = picker.shown.map((zone,index) => {
      const option = document.createElement("button"); option.type = "button"; option.id = `timezone-option-${index}`;
      option.setAttribute("role", "option"); option.setAttribute("tabindex", "-1"); option.setAttribute("aria-selected", String(zone === $("system-timezone").value));
      option.dataset.active = String(index === picker.active); option.textContent = zone;
      option.addEventListener("click", () => chooseTimeZone(zone)); return option;
    });
    if (!nodes.length) { const empty=document.createElement("p"); empty.textContent="未找到候选时区，可输入完整 IANA 名称后保存验证。"; nodes.push(empty); }
    $("timezone-options").replaceChildren(...nodes);
  }

  function chooseTimeZone(zone) {
    $("system-timezone").value = zone; timezoneForm.dirty = true; timezoneForm.message = timeZoneHasChanges() ? "有未保存的更改" : "";
    timezonePicker.open = false; renderTimeZone(); $("system-timezone").focus();
  }

  function settingsNavigationBusy() {
    return access.saving || timezoneForm.saving || connectionsUI.platforms.sending || !!connectionsUI.platforms.auth || !!connectionsUI.platforms.gitlabAuth || !!connectionsUI.platforms.scionAuth ||
      !!connectionsUI.platforms.larkStarting || !!connectionsUI.platforms.gitlabStarting || !!connectionsUI.platforms.scionStarting;
  }

  function renderSettingsNavigation() {
    if (!availableSettingsTabs().includes(settingsTab)) settingsTab = "general";
    $("general-settings").hidden = settingsTab !== "general";
    $("model-settings").hidden = settingsTab !== "models";
    $("platform-settings").hidden = settingsTab !== "platforms";
    $("access-settings").hidden = settingsTab !== "security";
    $("settings-tab-models").hidden = !state.managed;
    $("settings-tab-platforms").hidden = !state.managed;
    $("settings-navigation").setAttribute("aria-orientation", compactViewport(window) ? "horizontal" : "vertical");
    for (const tab of settingsTabs) {
      const button = $(`settings-tab-${tab}`), selected = settingsTab === tab;
      button.setAttribute("aria-selected", String(selected)); button.setAttribute("tabindex", selected ? "0" : "-1");
      button.disabled = settingsNavigationBusy();
    }
  }

  function selectSettingsTab(tab) {
    if (settingsNavigationBusy() || !availableSettingsTabs().includes(tab)) return;
    models.clearKey(); access.clear(); settingsTab = tab; timezonePicker.open = false; renderTimeZoneOptions(); renderSettingsNavigation();
    $("settings-panels").scrollTop = 0;
    if (tab === "models") void models.load();
    if (tab === "security") void access.load();
  }

  function applyTimeZone(data) {
    if (!systemTime.update(data)) return;
    for (const [id, input] of timeInputs) {
      if ($(id).validity?.badInput) { input.edit("", true); input.rebase(); }
      else $(id).value = input.rebase();
    }
    for (const prefix of ["lark", "gitlab"]) $(`${prefix}-backfill-start`).max = systemTime.local(Date.now());
    $("policy-expires-hint").textContent = `按系统时区 ${systemTime.zone || "（异常）"} 输入；留空则不设到期时间。`;
    renderTimeZone();
    const top = $("timeline").scrollTop;
    conversationUI.renderMessages(); $("timeline").scrollTop = top;
    for (const element of document.querySelectorAll("[data-system-time]")) element.refreshSystemTime?.();
    roomsUI.renderRooms(); workspaceUI.renderWorkspace();
    if (policyUI.policyEditor && ($("policy-expires").value || $("policy-expires").validity?.badInput)) {
      try { readTimeInput("policy-expires"); }
      catch (error) { $("policy-status").textContent = error.message; }
    }
  }

  async function openSettings(tab = settingsTab) {
    notificationsUI.invalidateNavigation();
    if (state.managed ? !state.catalog : !state.info) return;
    connectionsUI.showPlatformList(); settingsTab = availableSettingsTabs().includes(tab) ? tab : "general";
    if (settingsTab === "security") void access.load();
    timezonePicker.open = false; renderTimeZone(); $("settings-dialog").showModal(); void models.load(); runtime.navigationChanged(); if(state.managed) {await connectionsUI.loadPlatforms();await onboardingUI.restoreOnboarding();}
  }

  async function closeSettings() {
    if (access.saving) return;
    if ($("close-settings").disabled) return;
    access.clear();
    await connectionsUI.cancelPlatformAuth(); await onboardingUI.onboardingAction("status",{discardUnconnected:true}); $("settings-dialog").close();
  }

  function showPlatforms() { settingsTab = "platforms"; renderSettingsNavigation(); }

  function mount() {
    models.mount();
    access.mount();
    try { browserTimeZone = Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch { /* Detection is optional. */ }

    for (const [id, input] of timeInputs) {
      const edit = () => input.edit($(id).value, $(id).validity?.badInput);
      $(id).addEventListener("input", edit);
      $(id).addEventListener("change", edit);
    }

    for (const tab of settingsTabs) {
      $(`settings-tab-${tab}`).addEventListener("click", () => selectSettingsTab(tab));
      $(`settings-tab-${tab}`).addEventListener("keydown", event => {
        if (!["ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key) || settingsNavigationBusy()) return;
        event.preventDefault();
        const tabs = availableSettingsTabs();
        const direction = ["ArrowUp", "ArrowLeft"].includes(event.key) ? -1 : 1;
        const next = event.key === "Home" ? tabs[0] : event.key === "End" ? tabs.at(-1) : tabs[(tabs.indexOf(tab) + direction + tabs.length) % tabs.length];
        selectSettingsTab(next); $(`settings-tab-${next}`).focus();
      });
    }

    $("system-timezone").addEventListener("input", () => {
      timezoneForm.dirty = true; timezoneForm.message = timeZoneHasChanges() ? "有未保存的更改" : ""; renderTimeZoneFeedback();
      $("timezone-save").disabled = !canSaveTimeZone();
      timezonePicker.open = true; timezonePicker.query = $("system-timezone").value; timezonePicker.active = -1; renderTimeZoneOptions();
    });

    $("system-timezone").addEventListener("click", () => {
      if (timezoneForm.saving || timezonePicker.open) return;
      timezonePicker.open = true; timezonePicker.query = ""; timezonePicker.active = -1; renderTimeZoneOptions();
    });

    $("timezone-picker-toggle").addEventListener("click", () => { timezonePicker.open = !timezonePicker.open; timezonePicker.query = ""; timezonePicker.active = -1; renderTimeZoneOptions(); });

    $("timezone-picker").addEventListener("keydown", event => {
      if (event.key !== "Escape" || !timezonePicker.open) return;
      event.preventDefault(); event.stopPropagation();
      timezonePicker.open = false; renderTimeZoneOptions();
      $("timezone-picker-toggle").focus();
    });

    $("system-timezone").addEventListener("keydown", event => {
      const picker = timezonePicker;
      if (event.key === "Escape" && picker.open) { event.preventDefault(); event.stopPropagation?.(); picker.open = false; renderTimeZoneOptions(); }
      else if (["ArrowDown", "ArrowUp"].includes(event.key)) {
        event.preventDefault(); if (!picker.open) { picker.open = true; picker.query = ""; }
        const count = picker.candidates.filter(zone => zone.toLowerCase().includes(picker.query.trim().toLowerCase())).length;
        picker.active = count ? (picker.active + (event.key === "ArrowDown" ? 1 : picker.active < 0 ? 0 : -1) + count) % count : -1;
        renderTimeZoneOptions(); if (picker.active >= 0) $(`timezone-option-${picker.active}`).scrollIntoView({block:"nearest"});
      } else if (event.key === "Enter" && picker.open && picker.active >= 0) { event.preventDefault(); chooseTimeZone(picker.shown[picker.active]); }
    });

    document.addEventListener("pointerdown", event => { if (timezonePicker.open && !event.target.closest?.("#timezone-picker")) { timezonePicker.open = false; renderTimeZoneOptions(); } });

    $("timezone-browser").addEventListener("click", () => { $("system-timezone").value = browserTimeZone; timezoneForm.dirty = true; timezoneForm.message = "已填入浏览器时区，保存后生效。"; renderTimeZone(); });

    $("timezone-form").addEventListener("submit", async event => {
      event.preventDefault();
      if (!canSaveTimeZone()) return;
      const identity = state.managed ? {service_epoch:systemTime.epoch} : {...state.info};
      const epoch = systemTime.epoch;
      timezoneForm.saving = true; timezoneForm.message = ""; renderTimeZone();
      try {
        const data = await request("/v1/settings/save", {...identity, caller:{kind:"human",id:state.operator}, expected_version:systemTime.version, time_zone:$("system-timezone").value.trim()});
        if (epoch !== systemTime.epoch) return;
        applyTimeZone({...data, service_epoch:epoch});
        timezoneForm.dirty = false;
        timezoneForm.message = `已保存，当前生效：${systemTime.zone}。适用于所有会话；Agent 从下一回合生效。`;
      } catch (error) {
        if (epoch !== systemTime.epoch) return;
        timezoneForm.message = `${error.code === "conflict" ? "设置已在其他页面或设备更改，草稿已保留，请核对当前值后重新保存。" : "保存失败，草稿已保留："}${error.message}`;
        if (error.code === "conflict") {
          try { const current = await request("/v1/settings"); if (current.service_epoch === epoch && systemTime.epoch === epoch) applyTimeZone(current); } catch { /* Keep the draft for explicit retry. */ }
        }
      } finally { timezoneForm.saving = false; renderTimeZone(); }
    });

    $("open-settings").addEventListener("click", () => openSettings());

    $("open-general-settings").addEventListener("click", () => openSettings());

    $("close-settings").addEventListener("click", closeSettings);

    dismissOnBackdrop($("settings-dialog"), closeSettings);

    $("settings-dialog").addEventListener("cancel", event => {
      if (timezonePicker.open) {
        event.preventDefault(); timezonePicker.open = false; renderTimeZoneOptions(); $("timezone-picker-toggle").focus();
      } else if (connectionsUI.platforms.sending || access.saving) event.preventDefault();
    });

    $("settings-dialog").addEventListener("close", () => { access.clear(); models.clearKey(); timezonePicker.open = false; renderTimeZoneOptions(); void connectionsUI.cancelPlatformAuth(); $("gitlab-client-secret").value = ""; connectionsUI.platforms.formVersion++; connectionsUI.platforms.generation++;
      if ($("notification-detail").open) { $(workspaceUI.workspaceView?.notificationChannel === "messages" ? "lark-manage-platforms" : "gitlab-manage-platforms").focus(); void workspaceUI.loadWorkspacePlatforms(); }
      else if($("activity-dialog").open){$("member-notification-add-toggle").focus({preventScroll:true});void workspaceUI.loadWorkspacePlatforms();if(membersUI.memberToolsView && membersUI.toolsCurrent(membersUI.memberToolsView))void membersUI.loadCLITools(membersUI.memberToolsView);}
      else $(state.managed ? "user-menu-toggle" : "open-general-settings").focus(); });
  }

  return {
    mount,
    openMemberModel: models.openMember,
    showPlatforms,
    systemTime,
    readTimeInput,
    fillTimeInput,
    formatDate,
    openSettings,
    renderSettingsNavigation,
    bindSystemTime,
    applyTimeZone,
    closeSettings,
    get settingsTab() { return settingsTab; },
  };
}

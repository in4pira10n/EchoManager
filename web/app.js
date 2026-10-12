import { createEchoButton } from "/kit/components/button/index.js";
import { createEchoLibraryCard } from "/kit/components/library-card/index.js";
import { createEchoTabs } from "/kit/components/tab/index.js";
import { createEchoField } from "/kit/components/input/index.js";
import { createEchoModal } from "/kit/components/modal/index.js";
import { createEchoMetric } from "/kit/components/metric/index.js";
import { createEchoSettingRow } from "/kit/components/setting-row/index.js";

const $ = (selector, root = document) => root.querySelector(selector);
const SETTINGS_CONFIG = window.ECHO_MANAGER_SETTINGS;
const taskList = $("#em-task-list");
const feedback = $("#em-toast-stack");
const MANAGER_PREFERENCES_KEY = SETTINGS_CONFIG.storageKeys.managerPreferences;
const SETTINGS_PANELS_KEY = SETTINGS_CONFIG.storageKeys.settingsPanels;
const ACCENT_PREFERENCE_KEY = SETTINGS_CONFIG.storageKeys.accentColor;
const DEFAULT_ACCENT_COLOR = SETTINGS_CONFIG.defaults.accentColor;

function loadAccentPreference() {
  try {
    const saved = localStorage.getItem(ACCENT_PREFERENCE_KEY) || DEFAULT_ACCENT_COLOR;
    return /^#[0-9a-f]{6}$/i.test(saved) ? saved.toLowerCase() : DEFAULT_ACCENT_COLOR;
  } catch {
    return DEFAULT_ACCENT_COLOR;
  }
}

function loadManagerPreferences() {
  const defaults = SETTINGS_CONFIG.defaults.managerPreferences;
  try {
    const saved = JSON.parse(localStorage.getItem(MANAGER_PREFERENCES_KEY) || "{}");
    return {
      confirmStop: typeof saved.confirmStop === "boolean" ? saved.confirmStop : defaults.confirmStop,
    };
  } catch {
    return defaults;
  }
}

function loadSettingsPanelState() {
  const defaults = SETTINGS_CONFIG.defaults.settingsPanels;
  try {
    const saved = JSON.parse(localStorage.getItem(SETTINGS_PANELS_KEY) || "{}");
    return Object.fromEntries(Object.entries(defaults).map(([key, expanded]) => [
      key,
      typeof saved[key] === "boolean" ? saved[key] : expanded,
    ]));
  } catch {
    return { ...defaults };
  }
}

function persistSettingsPanelState() {
  try { localStorage.setItem(SETTINGS_PANELS_KEY, JSON.stringify(state.settingsPanels)); } catch {}
}

const state = { instances: [], signature: "", devices: [], deviceSignature: "", activeId: "", expandedInstanceIds: new Set(), pendingActions: new Map(), runtimeTimer: null, currentView: 0, theme: SETTINGS_CONFIG.defaults.theme, accentColor: loadAccentPreference(), currentUser: "", modal: null, deleteModal: null, stopConfirmModal: null, logoutModal: null, managerShutdownModal: null, cropModal: null, cropCleanup: null, deviceModal: null, deviceDeleteModal: null, iconSources: new Map(), preferences: loadManagerPreferences(), settingsPanels: loadSettingsPanelState(), refreshing: false };
const systemTheme = window.matchMedia("(prefers-color-scheme: dark)");

function showFeedback(message, kind = "info") {
  if (!message) return;
  const toast = document.createElement("div");
  toast.className = "toast em-toast";
  toast.dataset.kind = kind;
  toast.textContent = message;
  feedback.append(toast);
  window.requestAnimationFrame(() => toast.classList.add("show"));
  const duration = kind === "error" ? 4200 : kind === "success" ? 2800 : 2200;
  window.setTimeout(() => {
    toast.classList.remove("show");
    window.setTimeout(() => toast.remove(), 220);
  }, duration);
}

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (options.body !== undefined) headers.set("Content-Type", "application/json");
  const response = await fetch(path, { ...options, headers, credentials: "same-origin", cache: "no-store" });
  if (response.status === 204) return null;
  const payload = await response.json().catch(() => ({}));
  if (response.status === 401) window.location.assign("/login");
  if (!response.ok) throw new Error(payload.error || `请求失败 (${response.status})`);
  return payload;
}

function makeIconButton({ icon, label, className = "", color = null, variant = "default", disabled = false, onClick = null }) {
  return createEchoButton({
    icon,
    label,
    ariaLabel: label,
    title: label,
    size: "icon",
    color,
    variant,
    disabled,
    className,
    onClick,
  });
}

function createEmptyState() {
  const emptyState = document.createElement("div");
  emptyState.className = "em-task-empty-state";
  emptyState.setAttribute("role", "status");
  emptyState.setAttribute("aria-label", "暂无已登记的服务实例");
  const icon = document.createElement("img");
  icon.src = "/kit/SFSymbols/square.dashed.svg";
  icon.alt = "";
  icon.setAttribute("aria-hidden", "true");
  emptyState.append(icon);
  return emptyState;
}

function statusLabel(status) {
  return ({
    stopped: "已停止",
    starting: "启动中",
    running: "运行中",
    stopping: "停止中",
    exception: "异常",
    unavailable: "不可用",
  })[status] || "未知状态";
}

function cardActions(instance) {
  const actions = document.createElement("div");
  actions.className = "em-task-actions echo-modal__footer";
  const processOwned = instance.pid > 0;
  const isActive = processOwned || ["starting", "running", "stopping"].includes(instance.status);
  const action = isActive ? "stop" : "start";
  const actionBusy = state.pendingActions.get(instance.id) === action
    || instance.status === "stopping"
    || (instance.status === "starting" && !processOwned);
  actions.append(
    makeIconButton({
      icon: action === "stop" ? "stop.fill" : "play.fill",
      label: action === "stop" ? "停止" : "启动",
      color: action === "stop" ? "red" : "blue",
      className: `em-task-action em-task-action--${action}`,
      disabled: actionBusy || (action === "start" ? instance.status === "unavailable" : !processOwned),
      onClick: (event) => runAction(instance, action, event),
    }),
    makeIconButton({
      icon: "pencil", label: "编辑", color: "green", className: "em-task-action em-task-action--edit",
      disabled: isActive || processOwned,
      onClick: () => openInstanceEditor(instance),
    }),
    makeIconButton({
      icon: "arrow.turn.up.right", label: "打开页面", color: "gray", className: "em-task-action em-task-action--open",
      disabled: instance.status !== "running",
      onClick: () => openInstancePage(instance),
    }),
  );
  return actions;
}

function formatRuntime(startedAt) {
  const start = Date.parse(startedAt || "");
  if (!Number.isFinite(start)) return "未运行";
  const totalSeconds = Math.max(0, Math.floor((Date.now() - start) / 1000));
  const days = Math.floor(totalSeconds / 86_400);
  const hours = Math.floor((totalSeconds % 86_400) / 3_600);
  const minutes = Math.floor((totalSeconds % 3_600) / 60);
  const seconds = totalSeconds % 60;
  const clock = [hours, minutes, seconds].map((part) => String(part).padStart(2, "0")).join(":");
  return days ? `${days}天 ${clock}` : clock;
}

function createInstanceDetails(instance) {
  const details = document.createElement("div");
  details.className = "em-task-details";
  details.setAttribute("aria-hidden", "true");
  details.inert = true;

  const metrics = document.createElement("div");
  metrics.className = "em-task-details-grid";
  const running = instance.pid > 0;
  const runtime = createEchoMetric({
    icon: "clock",
    label: "运行时长",
    value: running ? formatRuntime(instance.startedAt) : "未运行",
  });
  if (running && instance.startedAt) runtime.dataset.runtimeStartedAt = instance.startedAt;

  metrics.append(
    createEchoMetric({ icon: "waveform.path.ecg", label: "运行状态", value: statusLabel(instance.status) }),
    runtime,
    createEchoMetric({ icon: "number", label: "进程 PID", value: instance.pid ? `PID ${instance.pid}` : "未分配" }),
    createEchoMetric({ icon: "network", label: "服务端口", value: `端口 ${instance.actualPort || instance.configuredPort}` }),
  );
  details.append(metrics);

  if (instance.error && ["exception", "unavailable"].includes(instance.status)) {
    const error = document.createElement("section");
    error.className = "em-task-error";
    const message = document.createElement("div");
    message.className = "em-task-error-message";
    message.textContent = instance.error;
    error.append(message);
    details.append(error);
  }

  return details;
}

function installDetailsLifecycle(details, instanceID) {
  details.addEventListener("transitionend", (event) => {
    if (event.target !== details || event.propertyName !== "height") return;
    if (state.expandedInstanceIds.has(instanceID)) {
      if (!details.classList.contains("is-closing")) details.style.height = "auto";
      return;
    }
    if (details.classList.contains("is-closing")) {
      details.remove();
      syncRuntimeTimer();
    }
  });
}

function toggleInstanceDetails(card, instance, expanded) {
  let details = card.querySelector(".em-task-details");
  const rowGap = Number.parseFloat(getComputedStyle(card).rowGap) || 0;

  if (expanded) {
    if (!details) {
      details = createInstanceDetails(instance);
      installDetailsLifecycle(details, instance.id);
      details.classList.add("is-expanded");
      details.setAttribute("aria-hidden", "false");
      details.inert = false;
      details.style.height = "0px";
      details.style.marginTop = `${-rowGap}px`;
      details.style.opacity = "0";
      card.append(details);
      void details.offsetHeight;
    } else {
      const currentHeight = details.getBoundingClientRect().height;
      const style = getComputedStyle(details);
      details.style.height = `${currentHeight}px`;
      details.style.marginTop = style.marginTop;
      details.style.opacity = style.opacity;
      details.classList.remove("is-closing");
      details.classList.add("is-expanded");
      details.setAttribute("aria-hidden", "false");
      details.inert = false;
      void details.offsetHeight;
    }

    const style = getComputedStyle(details);
    const borderHeight = (Number.parseFloat(style.borderTopWidth) || 0) + (Number.parseFloat(style.borderBottomWidth) || 0);
    const targetHeight = details.scrollHeight + borderHeight;
    window.requestAnimationFrame(() => {
      if (!state.expandedInstanceIds.has(instance.id) || !details.isConnected) return;
      details.style.height = `${targetHeight}px`;
      details.style.marginTop = "0px";
      details.style.opacity = "1";
    });
    return;
  }

  if (!details) return;
  const currentHeight = details.getBoundingClientRect().height;
  if (currentHeight < 1) {
    details.remove();
    return;
  }
  const style = getComputedStyle(details);
  details.style.height = `${currentHeight}px`;
  details.style.marginTop = style.marginTop;
  details.style.opacity = style.opacity;
  details.setAttribute("aria-hidden", "true");
  details.inert = true;
  details.classList.add("is-closing");
  void details.offsetHeight;
  window.requestAnimationFrame(() => {
    if (state.expandedInstanceIds.has(instance.id) || !details.isConnected) return;
    details.style.height = "0px";
    details.style.marginTop = `${-rowGap}px`;
    details.style.opacity = "0";
  });
}

function updateExpandedRuntimes() {
  taskList.querySelectorAll(".em-task-details.is-expanded:not(.is-closing) [data-runtime-started-at]").forEach((metric) => {
    const value = metric.querySelector(".echo-metric__value");
    if (value) value.textContent = formatRuntime(metric.dataset.runtimeStartedAt);
  });
}

function syncRuntimeTimer() {
  const hasRunningDetails = taskList.querySelector(".em-task-details.is-expanded:not(.is-closing) [data-runtime-started-at]");
  if (hasRunningDetails && state.runtimeTimer === null) {
    state.runtimeTimer = window.setInterval(updateExpandedRuntimes, 1000);
  } else if (!hasRunningDetails && state.runtimeTimer !== null) {
    window.clearInterval(state.runtimeTimer);
    state.runtimeTimer = null;
  }
}

function openInstancePage(instance) {
  const remote = instance.deviceId && instance.deviceId !== "local";
  const rawHost = remote ? instance.deviceAddress : (window.location.hostname || "127.0.0.1");
  if (!rawHost) {
    showFeedback("目标设备没有可用地址，无法打开服务页面。", "error");
    return;
  }
  const normalizedHost = String(rawHost).trim().replace(/^\[|\]$/g, "");
  const host = normalizedHost.includes(":") ? `[${normalizedHost}]` : normalizedHost;
  const protocol = window.location.protocol === "https:" ? "https:" : "http:";
  window.open(`${protocol}//${host}:${instance.actualPort || instance.configuredPort}`, "_blank", "noopener,noreferrer");
}

function renderInstances(instances) {
  $("#em-current-origin").textContent = window.location.origin;
  $("#em-instance-count").textContent = `${instances.length}${SETTINGS_CONFIG.copy.manager.instanceCountSuffix}`;
  const signature = instanceRenderSignature(instances);
  if (signature === state.signature) return;
  state.signature = signature;
  taskList.replaceChildren();
  if (!instances.length) {
    taskList.classList.add("em-task-list--empty");
    state.expandedInstanceIds.clear();
    for (const cached of state.iconSources.values()) URL.revokeObjectURL(cached.objectURL);
    state.iconSources.clear();
    taskList.append(createEmptyState());
    syncRuntimeTimer();
    return;
  }
  taskList.classList.remove("em-task-list--empty");
  const instanceIDs = new Set(instances.map((instance) => instance.id));
  for (const id of state.expandedInstanceIds) {
    if (!instanceIDs.has(id)) state.expandedInstanceIds.delete(id);
  }
  for (const [id, cached] of state.iconSources) {
    if (!instanceIDs.has(id)) {
      URL.revokeObjectURL(cached.objectURL);
      state.iconSources.delete(id);
    }
  }
  if (!instances.some((item) => item.id === state.activeId)) state.activeId = instances[0].id;

  for (const instance of instances) {
    const card = createEchoLibraryCard({
      logo: "/static/assets/task-compass.png",
      icon: "app.badge",
      title: instance.name,
      addition: `${instance.deviceName || "本机"} · ${instance.executablePath}`,
      attachment: [],
      content: [() => cardActions(instance)],
      active: state.activeId === instance.id,
      available: instance.status !== "unavailable",
      onClick: (event) => {
        if (event.target.closest("button, .em-task-details")) return;
        state.activeId = instance.id;
        taskList.querySelectorAll(".em-task-card").forEach((itemCard) => {
          const active = itemCard.dataset.instanceId === state.activeId;
          itemCard.classList.toggle("active", active);
          itemCard.setAttribute("aria-pressed", String(active));
        });
        const expanded = !state.expandedInstanceIds.has(instance.id);
        if (expanded) state.expandedInstanceIds.add(instance.id);
        else state.expandedInstanceIds.delete(instance.id);
        card.classList.toggle("is-expanded", expanded);
        card.setAttribute("aria-expanded", String(expanded));
        toggleInstanceDetails(card, instance, expanded);
        syncRuntimeTimer();
      },
    });
    card.classList.add("em-task-card");
    const cardState = instance.status === "running"
      ? "running"
      : ["starting", "stopping"].includes(instance.status) ? "starting" : "stopped";
    card.classList.add(`em-task-card--${cardState}`);
    card.dataset.instanceId = instance.id;
    const detailsExpanded = state.expandedInstanceIds.has(instance.id);
    card.classList.toggle("is-expanded", detailsExpanded);
    card.setAttribute("aria-expanded", String(detailsExpanded));
    if (detailsExpanded) {
      const details = createInstanceDetails(instance);
      installDetailsLifecycle(details, instance.id);
      details.classList.add("is-expanded");
      details.setAttribute("aria-hidden", "false");
      details.inert = false;
      details.style.height = "auto";
      details.style.marginTop = "0px";
      details.style.opacity = "1";
      card.append(details);
    }
    card.setAttribute("aria-label", `${instance.name}，${statusLabel(instance.status)}，端口 ${instance.configuredPort}，${instance.executablePath}`);
    card.querySelector(".echo-library-card__name")?.setAttribute("data-port", String(instance.configuredPort));
    card.querySelector(".echo-library-card__name")?.insertAdjacentHTML("afterend", `<span class="em-status em-status--${escapeAttribute(instance.status)}">${escapeHTML(statusLabel(instance.status))}</span>`);
    loadTaskIcon(instance).then((iconURL) => {
      if (!iconURL || !card.isConnected) return;
      const image = card.querySelector(".echo-library-card__logo img");
      if (image) image.src = iconURL;
    });
    installTaskIconEditor(card, instance);
    taskList.append(card);
  }
  syncRuntimeTimer();
}

function instanceRenderSignature(instances) {
  const pendingActions = [...state.pendingActions.entries()].sort(([left], [right]) => left.localeCompare(right));
  return JSON.stringify({ instances, pendingActions });
}

async function loadTaskIcon(instance) {
  if (!instance?.iconURL) return null;
  const cached = state.iconSources.get(instance.id);
  if (cached?.url === instance.iconURL) return cached.objectURL;
  if (cached) {
    URL.revokeObjectURL(cached.objectURL);
    state.iconSources.delete(instance.id);
  }
  try {
    const response = await fetch(instance.iconURL, { credentials: "same-origin", cache: "force-cache" });
    if (!response.ok) throw new Error(`读取任务图标失败 (${response.status})`);
    const objectURL = URL.createObjectURL(await response.blob());
    const existing = state.iconSources.get(instance.id);
    if (existing) URL.revokeObjectURL(existing.objectURL);
    state.iconSources.set(instance.id, { url: instance.iconURL, objectURL });
    return objectURL;
  } catch (error) {
    console.warn("读取任务图标失败", error);
    return null;
  }
}

function installTaskIconEditor(card, instance) {
  const logo = card.querySelector(".echo-library-card__logo");
  if (!logo) return;
  installTaskIconPicker(logo, instance);
}

function installTaskIconPicker(trigger, instance) {
  trigger.classList.add("em-task-logo-editable");
  trigger.tabIndex = 0;
  trigger.setAttribute("role", "button");
  trigger.setAttribute("aria-label", `更换${instance.name}的图标`);
  trigger.title = "点击更换任务图标";
  const openPicker = (event) => {
    event?.preventDefault();
    event?.stopPropagation();
    const picker = document.createElement("input");
    picker.type = "file";
    picker.accept = "image/*";
    picker.hidden = true;
    picker.addEventListener("change", () => {
      const file = picker.files?.[0];
      picker.remove();
      if (!file) return;
      if (!file.type.startsWith("image/")) {
        showFeedback("请选择图片文件。", "error");
        return;
      }
      if (file.size > 20 * 1024 * 1024) {
        showFeedback("图片不能超过 20 MiB。", "error");
        return;
      }
      openTaskIconCrop(instance, file, trigger);
    }, { once: true });
    picker.addEventListener("cancel", () => picker.remove(), { once: true });
    document.body.append(picker);
    picker.click();
  };
  trigger.addEventListener("click", openPicker);
  trigger.addEventListener("keydown", (event) => {
    if (event.key !== "Enter" && event.key !== " ") return;
    openPicker(event);
  });
}

function openTaskIconCrop(instance, file, trigger) {
  state.cropModal?.echoModal?.destroy();
  const objectURL = URL.createObjectURL(file);
  const content = document.createElement("div");
  content.className = "em-icon-crop-content";
  const stage = document.createElement("div");
  stage.className = "em-icon-crop-stage";
  stage.setAttribute("aria-label", "拖动图片调整任务图标构图");
  const image = document.createElement("img");
  image.alt = "任务图标裁剪预览";
  stage.append(image);

  const rangeLabel = document.createElement("label");
  rangeLabel.className = "em-icon-crop-range-label";
  rangeLabel.htmlFor = "em-icon-crop-zoom";
  rangeLabel.textContent = "缩放";
  const range = document.createElement("input");
  range.id = "em-icon-crop-zoom";
  range.type = "range";
  range.min = "1";
  range.max = "3";
  range.step = "0.01";
  range.value = "1";
  const zoomValue = document.createElement("output");
  zoomValue.htmlFor = range.id;
  zoomValue.textContent = "100%";
  const toolbar = document.createElement("div");
  toolbar.className = "em-icon-crop-toolbar";
  const zoomOut = makeIconButton({ icon: "minus", label: "缩小", className: "em-icon-crop-tool em-icon-crop-tool--zoom-out" });
  const zoomIn = makeIconButton({ icon: "plus", label: "放大", className: "em-icon-crop-tool" });
  const reset = makeIconButton({ icon: "arrow.counterclockwise", label: "重置", className: "em-icon-crop-tool" });
  const rangeGroup = document.createElement("div");
  rangeGroup.className = "em-icon-crop-range";
  rangeGroup.append(rangeLabel, range, zoomValue);
  toolbar.append(zoomOut, rangeGroup, zoomIn, reset);
  const hint = document.createElement("p");
  hint.className = "em-icon-crop-hint";
  hint.textContent = "拖动图片调整位置，也可用滑杆或双指缩放；图标将以圆形显示。";
  content.append(stage, toolbar, hint);

  let cropModal;
  let disposed = false;
  const crop = {
    x: 0, y: 0, zoom: 1, dragging: false, sx: 0, sy: 0, ox: 0, oy: 0,
    naturalWidth: 0, naturalHeight: 0, baseScale: 1, stageSize: 0,
    pointers: new Map(), pinchDistance: 0, pinchZoom: 1,
  };
  const clamp = (value, min, max) => Math.min(max, Math.max(min, value));
  const pointerDistance = () => {
    const points = [...crop.pointers.values()];
    return points.length < 2 ? 0 : Math.hypot(points[0].x - points[1].x, points[0].y - points[1].y);
  };
  const clampOffset = () => {
    if (!crop.naturalWidth || !crop.naturalHeight || !crop.stageSize) return;
    const width = crop.naturalWidth * crop.baseScale * crop.zoom;
    const height = crop.naturalHeight * crop.baseScale * crop.zoom;
    const centeredX = (crop.stageSize - width) / 2;
    const centeredY = (crop.stageSize - height) / 2;
    crop.x = clamp(crop.x, crop.stageSize - width - centeredX, -centeredX);
    crop.y = clamp(crop.y, crop.stageSize - height - centeredY, -centeredY);
  };
  const apply = () => {
    if (!crop.naturalWidth || !crop.naturalHeight) return;
    crop.stageSize = stage.getBoundingClientRect().width || 280;
    crop.baseScale = Math.max(crop.stageSize / crop.naturalWidth, crop.stageSize / crop.naturalHeight);
    clampOffset();
    const width = crop.naturalWidth * crop.baseScale * crop.zoom;
    const height = crop.naturalHeight * crop.baseScale * crop.zoom;
    image.style.width = `${width}px`;
    image.style.height = `${height}px`;
    image.style.left = `${(crop.stageSize - width) / 2 + crop.x}px`;
    image.style.top = `${(crop.stageSize - height) / 2 + crop.y}px`;
    range.value = String(crop.zoom);
    zoomValue.textContent = `${Math.round(crop.zoom * 100)}%`;
  };
  const setZoom = (value) => {
    crop.zoom = clamp(Number(value) || 1, Number(range.min), Number(range.max));
    apply();
  };
  const closeCrop = () => {
    if (disposed) return;
    disposed = true;
    URL.revokeObjectURL(objectURL);
    window.removeEventListener("resize", apply);
    state.cropCleanup = null;
    state.cropModal = null;
    cropModal?.echoModal?.destroy();
  };
  const cancel = makeTextAction({ label: "取消", icon: "xmark", color: "gray", onClick: closeCrop });
  const save = makeTextAction({ label: "使用图标", icon: "checkmark", color: "blue", onClick: saveCrop });
  cropModal = createEchoModal({
    id: "em-icon-crop-modal",
    icon: "photo",
    title: "裁剪任务图标",
    addition: "拖动并缩放图片，裁剪为正方形图标。",
    content,
    actions: [cancel, save],
    className: "em-icon-crop-modal",
    overlayClassName: "em-editor-overlay",
  });
  state.cropModal = cropModal;
  $("#em-modal-root").append(cropModal);
  cropModal.echoModal.open(trigger);
  image.addEventListener("load", () => {
    crop.naturalWidth = image.naturalWidth;
    crop.naturalHeight = image.naturalHeight;
    apply();
  }, { once: true });
  image.addEventListener("error", () => {
    showFeedback("无法读取所选图片。", "error");
    closeCrop();
  }, { once: true });
  image.src = objectURL;
  window.addEventListener("resize", apply);
  range.addEventListener("input", () => setZoom(range.value));
  zoomOut.addEventListener("click", () => setZoom(crop.zoom - 0.1));
  zoomIn.addEventListener("click", () => setZoom(crop.zoom + 0.1));
  reset.addEventListener("click", () => { crop.x = 0; crop.y = 0; setZoom(1); });
  stage.addEventListener("wheel", (event) => {
    event.preventDefault();
    setZoom(crop.zoom + (event.deltaY < 0 ? 0.08 : -0.08));
  }, { passive: false });
  stage.addEventListener("pointerdown", (event) => {
    event.preventDefault();
    stage.setPointerCapture(event.pointerId);
    crop.pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    if (crop.pointers.size > 1) {
      crop.dragging = false;
      crop.pinchDistance = pointerDistance();
      crop.pinchZoom = crop.zoom;
      return;
    }
    crop.dragging = true;
    crop.sx = event.clientX;
    crop.sy = event.clientY;
    crop.ox = crop.x;
    crop.oy = crop.y;
  });
  stage.addEventListener("pointermove", (event) => {
    if (!crop.pointers.has(event.pointerId)) return;
    crop.pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    if (crop.pointers.size > 1) {
      const distance = pointerDistance();
      if (crop.pinchDistance > 0 && distance > 0) setZoom(crop.pinchZoom * distance / crop.pinchDistance);
      return;
    }
    if (crop.dragging) {
      crop.x = crop.ox + event.clientX - crop.sx;
      crop.y = crop.oy + event.clientY - crop.sy;
      apply();
    }
  });
  const releasePointer = (event) => {
    crop.pointers.delete(event.pointerId);
    crop.dragging = false;
    try { stage.releasePointerCapture(event.pointerId); } catch {}
    if (crop.pointers.size === 1) {
      const point = [...crop.pointers.values()][0];
      crop.dragging = true;
      crop.sx = point.x;
      crop.sy = point.y;
      crop.ox = crop.x;
      crop.oy = crop.y;
    }
  };
  stage.addEventListener("pointerup", releasePointer);
  stage.addEventListener("pointercancel", releasePointer);
  state.cropCleanup = closeCrop;
  cropModal.addEventListener("click", (event) => {
    if (event.target === cropModal) window.setTimeout(closeCrop, 0);
  });

  async function saveCrop() {
    if (!crop.naturalWidth || !crop.naturalHeight || save.disabled) return;
    save.disabled = true;
    try {
      const size = 512;
      const scale = crop.baseScale * crop.zoom;
      const cropSize = Math.min(crop.naturalWidth, crop.naturalHeight, crop.stageSize / scale);
      const offsetX = (crop.stageSize - crop.naturalWidth * scale) / 2 + crop.x;
      const offsetY = (crop.stageSize - crop.naturalHeight * scale) / 2 + crop.y;
      const sourceX = clamp(-offsetX / scale, 0, crop.naturalWidth - cropSize);
      const sourceY = clamp(-offsetY / scale, 0, crop.naturalHeight - cropSize);
      const canvas = document.createElement("canvas");
      canvas.width = size;
      canvas.height = size;
      const context = canvas.getContext("2d");
      if (!context) throw new Error("无法创建图片裁剪画布。");
      context.drawImage(image, sourceX, sourceY, cropSize, cropSize, 0, 0, size, size);
      const iconData = canvas.toDataURL("image/png");
      const updated = await api(`/api/instances/${instance.id}/icon`, { method: "POST", body: JSON.stringify({ iconData }) });
      instance.iconURL = updated.iconURL;
      const iconURL = await loadTaskIcon(updated);
      const triggerImage = trigger?.querySelector("img");
      if (iconURL && triggerImage) triggerImage.src = iconURL;
      closeCrop();
      showFeedback("任务图标已更新。", "success");
      await refreshInstances(true);
    } catch (error) {
      showFeedback(error.message || "任务图标更新失败。", "error");
      save.disabled = false;
    }
  }
}

function escapeHTML(value) {
  return String(value).replace(/[&<>"']/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[char]);
}

function escapeAttribute(value) {
  return String(value).replace(/[^a-z0-9_-]/gi, "");
}

async function refreshInstances(silent = false, instanceID = "") {
  try {
    const query = instanceID ? `?instance=${encodeURIComponent(instanceID)}` : "";
    const response = await api(`/api/instances/refresh${query}`, { method: "POST" });
    state.instances = (response.instances || []).map((updated) => {
      const previous = state.instances.find((item) => item.id === updated.id);
      if (state.pendingActions.get(updated.id) === "start" && updated.status === "stopped" && previous?.status === "starting") {
        return {
          ...updated,
          status: "starting",
          pid: previous.pid || updated.pid,
          actualPort: previous.actualPort || updated.actualPort,
          startedAt: previous.startedAt || updated.startedAt,
          error: previous.error || "正在等待远端启动确认…",
        };
      }
      if (state.pendingActions.get(updated.id) === "start" && updated.pid > 0) {
        state.pendingActions.delete(updated.id);
      }
      return updated;
    });
    renderInstances(state.instances);
    return true;
  } catch (error) {
    if (!silent) showFeedback(error.message, "error");
    return false;
  } finally {
    if (state.signature) {
      const signature = instanceRenderSignature(state.instances);
      if (signature !== state.signature) renderInstances(state.instances);
    }
  }
}

function updateInstanceSnapshot(instance) {
  if (!instance?.id) return;
  const index = state.instances.findIndex((item) => item.id === instance.id);
  if (index < 0) return;
  state.instances = state.instances.map((item) => item.id === instance.id ? instance : item);
  renderInstances(state.instances);
}

function updateInstanceState(instanceID, changes) {
  const instance = state.instances.find((item) => item.id === instanceID);
  if (!instance) return;
  updateInstanceSnapshot({ ...instance, ...changes });
}

async function runAction(instance, action, event) {
  const button = event?.currentTarget;
  if (action === "stop" && state.preferences.confirmStop) {
    openStopConfirmation(instance, button);
    return;
  }
  await performAction(instance, action, button);
}

async function performAction(instance, action, button) {
  if (button) button.disabled = true;
  state.pendingActions.set(instance.id, action);
  showFeedback(action === "start" ? `正在启动 ${instance.name}，等待端口就绪…` : `正在停止 ${instance.name}…`, "info");
  updateInstanceState(instance.id, {
    status: action === "start" ? "starting" : "stopping",
    error: action === "start" ? "等待目标服务端口就绪…" : "正在停止远程进程…",
  });
  let startRequestPending = false;
  let startRefreshLoop = null;
  try {
    let result;
    if (action === "start") {
      startRequestPending = true;
      const startRequest = api(`/api/instances/${instance.id}/${action}`, { method: "POST" });
      startRefreshLoop = refreshWhileStartRequestIsPending(instance.id, () => startRequestPending);
      result = await startRequest;
      startRequestPending = false;
      const refreshedDuringStart = await startRefreshLoop;
      if (!refreshedDuringStart && result.instance) updateInstanceSnapshot(result.instance);
    } else {
      result = await api(`/api/instances/${instance.id}/${action}`, { method: "POST" });
      if (result.instance) {
        updateInstanceSnapshot(result.instance);
      } else if (result.status) {
        updateInstanceState(instance.id, {
          status: result.status,
          ...(result.status === "stopped" ? { pid: 0, actualPort: 0, startedAt: "", error: "" } : {}),
        });
      }
    }
    if (action === "start") {
      releaseStartActionIfProcessKnown(instance.id);
      const settled = await waitForStartup(instance.id);
      if (state.pendingActions.get(instance.id) === "stop") return;
      if (settled?.status === "running") {
        showFeedback(`${instance.name} 已运行。`, "success");
      } else if (settled?.status === "starting") showFeedback(`${instance.name} 仍在启动；可稍后手动刷新状态。`, "info");
      else if (settled?.status === "exception" || settled?.status === "unavailable") showFeedback(settled.error || `${instance.name} 启动失败。`, "error");
      else if (settled) showFeedback(`${instance.name} 当前状态：${statusLabel(settled.status)}。`, "info");
      else showFeedback(`${instance.name} 已提交启动请求，但暂时无法读取最新状态。`, "info");
      return;
    }
    showFeedback(`${instance.name} 已停止。`, "success");
  } catch (error) {
    startRequestPending = false;
    if (startRefreshLoop) await startRefreshLoop;
    if (action === "start") {
      if (state.pendingActions.get(instance.id) === "start") state.pendingActions.delete(instance.id);
      await refreshInstances(true, instance.id);
      if (state.pendingActions.get(instance.id) === "stop") return;
      const current = state.instances.find((item) => item.id === instance.id);
      if (current?.status === "running") {
        showFeedback(`${instance.name} 已运行。`, "success");
        return;
      }
    }
    showFeedback(error.message, "error");
    if (!await refreshInstances(true, instance.id)) updateInstanceSnapshot(instance);
  } finally {
    startRequestPending = false;
    if (state.pendingActions.get(instance.id) === action) state.pendingActions.delete(instance.id);
    renderInstances(state.instances);
    if (button) button.disabled = false;
  }
}

async function refreshWhileStartRequestIsPending(instanceID, isPending) {
  let refreshed = false;
  while (isPending()) {
    refreshed = await refreshInstances(true, instanceID) || refreshed;
    if (isPending()) await new Promise((resolve) => window.setTimeout(resolve, 800));
  }
  return refreshed;
}

function releaseStartActionIfProcessKnown(instanceID) {
  const instance = state.instances.find((item) => item.id === instanceID);
  const processKnown = instance?.pid > 0;
  if (!processKnown || state.pendingActions.get(instanceID) !== "start") return;
  state.pendingActions.delete(instanceID);
  renderInstances(state.instances);
}

async function waitForStartup(instanceID) {
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    if (state.pendingActions.get(instanceID) === "stop") {
      return state.instances.find((item) => item.id === instanceID) || null;
    }
    if (await refreshInstances(true, instanceID)) {
      const instance = state.instances.find((item) => item.id === instanceID);
      releaseStartActionIfProcessKnown(instanceID);
      if (state.pendingActions.get(instanceID) === "stop") return instance || null;
      if (!instance || instance.status !== "starting") return instance || null;
    }
    await new Promise((resolve) => window.setTimeout(resolve, 800));
  }
  await refreshInstances(true, instanceID);
  return state.instances.find((item) => item.id === instanceID) || null;
}

function dismissStopConfirmation() {
  const modal = state.stopConfirmModal;
  modal?.echoModal?.close();
  modal?.echoModal?.destroy();
  if (state.stopConfirmModal === modal) state.stopConfirmModal = null;
}

function dismissLogoutConfirmation() {
  const modal = state.logoutModal;
  modal?.echoModal?.close();
  modal?.echoModal?.destroy();
  if (state.logoutModal === modal) state.logoutModal = null;
}

function dismissManagerShutdownConfirmation() {
  const modal = state.managerShutdownModal;
  modal?.echoModal?.close();
  modal?.echoModal?.destroy();
  if (state.managerShutdownModal === modal) state.managerShutdownModal = null;
}

function openManagerShutdownConfirmation(sourceButton) {
  dismissManagerShutdownConfirmation();
  const content = document.createElement("p");
  content.className = "em-delete-confirm-copy";
  content.textContent = "关闭后，EchoManager 将停止响应所有设备的访问；已经启动的服务进程会继续运行。";
  const cancel = makeTextAction({
    label: "取消",
    icon: "xmark",
    color: "gray",
    onClick: dismissManagerShutdownConfirmation,
  });
  const confirm = makeTextAction({
    label: "关闭 EchoManager",
    icon: "power",
    color: "red",
    onClick: async (_event, button) => {
      button.disabled = true;
      try {
        await api("/api/manager/shutdown", { method: "POST" });
        dismissManagerShutdownConfirmation();
        showFeedback("EchoManager 正在关闭；已启动的服务进程会继续运行。", "info");
      } catch (error) {
        button.disabled = false;
        showFeedback(error.message, "error");
      }
    },
  });
  state.managerShutdownModal = createEchoModal({
    id: "em-manager-shutdown-modal",
    icon: "power",
    title: "关闭 EchoManager",
    addition: "此操作会停止管理服务，不会停止已启动的服务进程。",
    content,
    actions: [cancel, confirm],
    className: "em-confirm-modal",
    overlayClassName: "em-editor-overlay",
  });
  $("#em-modal-root").append(state.managerShutdownModal);
  state.managerShutdownModal.echoModal.open(sourceButton);
  const overlay = state.managerShutdownModal;
  overlay.addEventListener("click", (event) => {
    if (event.target !== overlay && !event.target.closest("[data-echo-modal-close]")) return;
    window.setTimeout(() => {
      if (state.managerShutdownModal === overlay && !overlay.classList.contains("is-open")) dismissManagerShutdownConfirmation();
    }, 0);
  });
}

function openLogoutConfirmation(sourceButton) {
  dismissLogoutConfirmation();
  const content = document.createElement("p");
  content.className = "em-delete-confirm-copy";
  content.textContent = "确定退出当前管理员账号吗？";
  const cancel = makeTextAction({
    label: "取消",
    icon: "xmark",
    color: "gray",
    onClick: dismissLogoutConfirmation,
  });
  const confirm = makeTextAction({
    label: "退出登录",
    icon: "arrow.turn.up.right",
    color: "blue",
    onClick: async (_event, button) => {
      button.disabled = true;
      try {
        await api("/api/auth/logout", { method: "POST" });
        dismissLogoutConfirmation();
        window.location.assign("/login");
      } catch (error) {
        button.disabled = false;
        showFeedback(error.message, "error");
      }
    },
  });
  state.logoutModal = createEchoModal({
    id: "em-logout-confirm-modal",
    icon: "info.circle",
    title: "退出登录",
    addition: "退出后需要重新登录才能继续管理服务。",
    content,
    actions: [cancel, confirm],
    className: "em-confirm-modal",
    overlayClassName: "em-editor-overlay",
  });
  $("#em-modal-root").append(state.logoutModal);
  state.logoutModal.echoModal.open(sourceButton);
  const overlay = state.logoutModal;
  overlay.addEventListener("click", (event) => {
    if (event.target !== overlay && !event.target.closest("[data-echo-modal-close]")) return;
    window.setTimeout(() => {
      if (state.logoutModal === overlay && !overlay.classList.contains("is-open")) dismissLogoutConfirmation();
    }, 0);
  });
}

function openStopConfirmation(instance, sourceButton) {
  dismissStopConfirmation();
  const content = document.createElement("p");
  content.className = "em-delete-confirm-copy";
  content.textContent = `确定停止“${instance.name}”吗？停止后，该服务将暂时无法访问。`;
  const cancel = makeTextAction({
    label: "取消",
    icon: "xmark",
    color: "gray",
    onClick: dismissStopConfirmation,
  });
  const stop = makeTextAction({
    label: "停止服务",
    icon: "stop.fill",
    color: "red",
    onClick: () => {
      dismissStopConfirmation();
      void performAction(instance, "stop", sourceButton);
    },
  });
  state.stopConfirmModal = createEchoModal({
    id: "em-stop-instance-modal",
    icon: "exclamationmark.triangle",
    title: "停止服务",
    addition: "请确认是否中断此服务。",
    content,
    actions: [cancel, stop],
    className: "em-confirm-modal",
    overlayClassName: "em-editor-overlay",
  });
  $("#em-modal-root").append(state.stopConfirmModal);
  state.stopConfirmModal.echoModal.open(sourceButton);
  const overlay = state.stopConfirmModal;
  overlay.addEventListener("click", (event) => {
    if (event.target !== overlay) return;
    window.setTimeout(() => {
      if (state.stopConfirmModal === overlay && !overlay.classList.contains("is-open")) dismissStopConfirmation();
    }, 0);
  });
}

function dismissDeleteConfirmation(returnToEditor = true) {
  const modal = state.deleteModal;
  modal?.echoModal?.close();
  modal?.echoModal?.destroy();
  if (state.deleteModal === modal) state.deleteModal = null;
  if (returnToEditor) state.modal?.echoModal?.open();
}

function openDeleteConfirmation(instance) {
  state.deleteModal?.echoModal?.destroy();
  const content = document.createElement("p");
  content.className = "em-delete-confirm-copy";
  content.textContent = `确定删除“${instance.name}”的管理登记吗？程序文件和运行日志会保留。`;
  const cancel = makeTextAction({
    label: "取消",
    icon: "xmark",
    color: "gray",
    onClick: () => dismissDeleteConfirmation(true),
  });
  let remove;
  remove = makeTextAction({
    label: "删除实例",
    icon: "trash",
    color: "red",
    onClick: () => deleteInstance(instance, remove),
  });
  state.deleteModal = createEchoModal({
    id: "em-delete-instance-modal",
    icon: "/static/assets/task-compass.png",
    title: "删除服务实例",
    addition: "此操作只移除 EchoManager 中的登记。",
    content,
    actions: [cancel, remove],
    className: "em-confirm-modal",
    overlayClassName: "em-editor-overlay",
  });
  state.modal?.echoModal?.close();
  $("#em-modal-root").append(state.deleteModal);
  state.deleteModal.echoModal.open();
  const overlay = state.deleteModal;
  loadTaskIcon(instance).then((iconURL) => {
    if (iconURL && state.deleteModal === overlay) {
      const image = overlay.querySelector(".echo-modal__mark img");
      if (image) image.src = iconURL;
    }
  });
  overlay.addEventListener("click", (event) => {
    if (event.target !== overlay) return;
    window.setTimeout(() => {
      if (state.deleteModal === overlay && !overlay.classList.contains("is-open")) dismissDeleteConfirmation(true);
    }, 0);
  });
}

async function deleteInstance(instance, button) {
  if (button) button.disabled = true;
  try {
    await api(`/api/instances/${instance.id}`, { method: "DELETE" });
    if (state.activeId === instance.id) state.activeId = "";
    state.modal?.echoModal?.destroy();
    state.modal = null;
    dismissDeleteConfirmation(false);
    showFeedback(`${instance.name} 已从管理器移除。`, "success");
    await refreshInstances(true);
  } catch (error) {
    showFeedback(error.message, "error");
    await refreshInstances(true);
    if (button) button.disabled = false;
  }
}

function parseArguments(value) {
  return String(value || "").trim().split(/\s+/).filter((arg) => arg.length > 0);
}

function parseEnvironment(value) {
  const result = {};
  const items = String(value || "").trim().split(/[,\s]+/);
  for (const item of items) {
    if (!item.trim()) continue;
    const separator = item.indexOf("=");
    if (separator < 1) throw new Error(`环境变量格式错误：${item}（需要 KEY=value）`);
    const key = item.slice(0, separator).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) throw new Error(`环境变量名称无效：${key}`);
    result[key] = item.slice(separator + 1);
  }
  return result;
}

function field(options) {
  return createEchoField(options);
}

function selectField({ label, name, options = [], value = "", hint = "", className = "" }) {
  const wrapper = document.createElement("label");
  wrapper.className = ["echo-field", "em-select-field", className].filter(Boolean).join(" ");
  const labelNode = document.createElement("span");
  labelNode.className = "echo-field__label";
  labelNode.textContent = label;
  const select = document.createElement("select");
  select.className = "echo-input";
  select.name = name || "";
  for (const option of options) {
    const optionNode = document.createElement("option");
    optionNode.value = String(option.value);
    optionNode.textContent = option.label;
    optionNode.selected = String(option.value) === String(value);
    select.append(optionNode);
  }
  wrapper.append(labelNode, select);
  if (hint) {
    const hintNode = document.createElement("span");
    hintNode.className = "echo-field__hint";
    hintNode.textContent = hint;
    wrapper.append(hintNode);
  }
  wrapper.echoField = { input: select };
  return wrapper;
}

function deviceStatusLabel(status) {
  return ({ online: "在线", offline: "离线", unknown: "未测试" })[status] || "未知";
}

function deviceIconName(device) {
  if (device.id === "local") {
    return "macmini.gen3.fill";
  }
  if (device.platform === "windows") return "pc";
  if (device.platform === "linux") return "server.rack";
  return "laptopcomputer";
}

function formatDeviceEndpoint(device) {
  if (!device?.address) return "没有地址";
  const address = String(device.address).includes(":") ? `[${device.address}]` : device.address;
  const port = device.transport === "local" ? "" : `:${device.port || (device.transport === "winrm" ? 5986 : 22)}`;
  return `${address}${port}`;
}

function createDeviceCard(device) {
  const card = document.createElement("button");
  card.type = "button";
  card.className = "em-network-device";
  if (device.id === "local") card.classList.add("em-network-device--local");
  card.dataset.deviceId = device.id;
  card.title = device.error || (device.id === "local" ? "本机无需远程连接配置" : "点击编辑设备");

  const icon = document.createElement("span");
  icon.className = "em-network-device-icon";
  const image = document.createElement("img");
  const iconName = deviceIconName(device);
  image.src = `/kit/SFSymbols/${iconName}.svg`;
  if (iconName === "macmini.gen3.fill") {
    image.addEventListener("error", () => {
      image.src = "/kit/SFSymbols/macmini.fill.svg";
    }, { once: true });
  }
  image.alt = "";
  icon.append(image);

  const name = document.createElement("strong");
  name.textContent = device.id === "local" ? `${device.name || "本机"}（本机）` : device.name;
  const endpoint = document.createElement("small");
  endpoint.textContent = formatDeviceEndpoint(device);
  const meta = document.createElement("small");
  meta.className = "em-network-device-meta";
  meta.textContent = `${device.instanceCount || 0} 个服务 · ${deviceStatusLabel(device.status)}`;
  card.append(icon, name, endpoint, meta);
  card.addEventListener("click", () => {
    if (device.id === "local") {
      showFeedback("本机设备由 EchoManager 自动管理，无需连接配置。", "info");
      return;
    }
    openDeviceEditor(device);
  });
  return card;
}

function renderDevices(devices) {
  const rail = $("#em-network-device-rail");
  if (!rail) return;
  $("#em-network-count").textContent = `当前共 ${devices.length} 台设备`;
  const signature = JSON.stringify(devices);
  if (signature === state.deviceSignature) return;
  state.deviceSignature = signature;
  rail.replaceChildren(...devices.map(createDeviceCard));
  const addAction = $("#em-network-add-action");
  if (addAction) {
    const addButton = document.createElement("button");
    addButton.type = "button";
    addButton.className = "em-settings-action em-network-add-button";
    addButton.textContent = "添加设备";
    addButton.addEventListener("click", () => openDeviceEditor());
    addAction.replaceChildren(addButton);
  }
}

async function refreshDevices(silent = false) {
  try {
    const response = await api("/api/devices");
    state.devices = response.devices || [];
    renderDevices(state.devices);
    return true;
  } catch (error) {
    if (!silent) showFeedback(error.message, "error");
    return false;
  }
}

function deviceOptions() {
  const devices = state.devices.length ? state.devices : [{ id: "local", name: "本机", platform: "unix", transport: "local" }];
  return devices.map((device) => ({
    value: device.id,
    label: device.id === "local" ? `${device.name || "本机"}（本机）` : `${device.name} · ${device.address}`,
  }));
}

function checkboxControl(label, checked = false) {
  const wrapper = document.createElement("label");
  wrapper.className = "em-settings-switch em-device-checkbox";
  const input = document.createElement("input");
  input.type = "checkbox";
  input.checked = checked;
  const copy = document.createElement("span");
  const title = document.createElement("strong");
  title.textContent = "跳过 WinRM 证书验证";
  const detail = document.createElement("small");
  detail.textContent = "仅限可信局域网";
  copy.append(title, detail);
  const track = document.createElement("span");
  track.className = "em-settings-switch-track";
  track.setAttribute("aria-hidden", "true");
  wrapper.append(copy, input, track);
  wrapper.setAttribute("aria-label", label);
  wrapper.echoControl = input;
  return wrapper;
}

function dismissDeviceDeleteConfirmation(returnToEditor = true) {
  const modal = state.deviceDeleteModal;
  modal?.echoModal?.close();
  modal?.echoModal?.destroy();
  if (state.deviceDeleteModal === modal) state.deviceDeleteModal = null;
  if (returnToEditor) state.deviceModal?.echoModal?.open();
}

function openDeviceDeleteConfirmation(device) {
  dismissDeviceDeleteConfirmation(false);
  const content = document.createElement("p");
  content.className = "em-delete-confirm-copy";
  content.textContent = `确定删除“${device.name}”吗？这只会移除设备登记，不会修改目标电脑。`;
  const cancel = makeTextAction({ label: "取消", icon: "xmark", color: "gray", onClick: () => dismissDeviceDeleteConfirmation(true) });
  const remove = makeTextAction({
    label: "删除设备",
    icon: "trash",
    color: "red",
    onClick: async (_event, button) => {
      button.disabled = true;
      try {
        await api(`/api/devices/${device.id}`, { method: "DELETE" });
        state.deviceModal?.echoModal?.destroy();
        state.deviceModal = null;
        dismissDeviceDeleteConfirmation(false);
        showFeedback(`${device.name} 已移除。`, "success");
        await refreshDevices(true);
      } catch (error) {
        button.disabled = false;
        showFeedback(error.message, "error");
      }
    },
  });
  state.deviceModal?.echoModal?.close();
  state.deviceDeleteModal = createEchoModal({
    id: "em-delete-device-modal",
    icon: "trash",
    title: "删除设备",
    addition: "运行中的实例或已关联实例的设备不能删除。",
    content,
    actions: [cancel, remove],
    className: "em-confirm-modal",
    overlayClassName: "em-editor-overlay",
  });
  $("#em-modal-root").append(state.deviceDeleteModal);
  state.deviceDeleteModal.echoModal.open();
}

function openDeviceEditor(device = null) {
  if (device?.id === "local") return;
  state.deviceModal?.echoModal?.destroy();
  const editing = Boolean(device);
  const values = {
    name: device?.name || "",
    address: device?.address || "",
    platform: device?.platform === "windows" ? "windows" : "unix",
    transport: device?.transport === "winrm" ? "winrm" : "ssh",
    port: String(device?.port || (device?.transport === "winrm" ? 5986 : 22)),
    username: device?.username || "",
    authType: device?.authType === "key" ? "key" : "password",
    keyPath: device?.keyPath || "",
  };
  const controls = {};
  const content = document.createElement("div");
  content.className = "em-form-content em-device-form-content";
  const connectionFields = document.createElement("div");
  connectionFields.className = "em-form-grid";
  controls.name = field({ label: "设备名称", name: "name", placeholder: "例如：客厅 Windows", required: true, value: values.name });
  controls.address = field({ label: "设备地址", name: "address", placeholder: "192.168.1.4 或主机名", required: true, value: values.address });
  controls.platform = selectField({ label: "目标系统", name: "platform", value: values.platform, options: [{ value: "unix", label: "macOS / Linux" }, { value: "windows", label: "Windows" }] });
  controls.transport = selectField({ label: "连接方式", name: "transport", value: values.transport, options: [{ value: "ssh", label: "SSH" }, { value: "winrm", label: "HTTPS WinRM" }] });
  controls.port = field({ label: "连接端口", name: "port", type: "number", value: values.port, attributes: { min: "1", max: "65535", inputmode: "numeric" } });
  controls.username = field({ label: "登录用户名", name: "username", value: values.username, autocomplete: "username", required: true });
  controls.authType = selectField({ label: "SSH 认证", name: "authType", value: values.authType, options: [{ value: "password", label: "密码" }, { value: "key", label: "私钥文件" }] });
  controls.password = field({ label: "密码", name: "password", type: "password", autocomplete: "new-password", hint: editing ? "留空以继续使用已保存密码。" : "凭据仅保存在 EchoManager 主机的数据目录。" });
  controls.keyPath = field({ label: "SSH 私钥路径", name: "keyPath", placeholder: "/Users/name/.ssh/id_ed25519", value: values.keyPath, hint: "这是 Manager 主机上的路径。" });
  controls.insecureTLS = checkboxControl("跳过 WinRM 证书验证（仅限可信局域网）", Boolean(device?.insecureTLS));
  connectionFields.append(controls.name, controls.address, controls.platform, controls.transport, controls.port, controls.username, controls.authType, controls.password, controls.keyPath, controls.insecureTLS);
  const testRow = document.createElement("div");
  testRow.className = "em-device-test-row";
  const testButton = document.createElement("button");
  testButton.type = "button";
  testButton.className = "em-settings-action em-device-test-action";
  testButton.textContent = "测试连接";
  testButton.addEventListener("click", () => {
    if (!editing) {
      showFeedback("请先保存设备，再测试连接。", "info");
      return;
    }
    void testDevice(device, testButton);
  });
  testRow.append(testButton);
  content.append(connectionFields, testRow);

  const cancel = makeTextAction({ label: "取消", icon: "xmark", color: "gray", onClick: () => state.deviceModal?.echoModal?.close() });
  const save = makeTextAction({ label: editing ? "保存修改" : "添加设备", icon: "checkmark", color: "blue", onClick: () => saveDevice() });
  const actions = editing ? [cancel, save] : [cancel, save];
  if (editing) {
    actions.splice(1, 0, makeTextAction({ label: "删除设备", icon: "trash", color: "red", onClick: () => openDeviceDeleteConfirmation(device) }));
  }
  state.deviceModal = createEchoModal({
    id: "em-device-modal",
    icon: "network",
    title: editing ? "编辑设备" : "添加设备",
    addition: "Manager 通过 SSH 或 HTTPS WinRM 控制目标电脑上的进程。",
    content,
    actions,
    className: editing ? "em-editor-modal em-editor-modal--editing em-device-modal" : "em-editor-modal em-device-modal",
    overlayClassName: "em-editor-overlay",
  });
  $("#em-modal-root").append(state.deviceModal);
  state.deviceModal.echoModal.open();

  const syncConnectionFields = () => {
    const platform = controls.platform.echoField.input.value;
    const transport = controls.transport.echoField.input.value;
    const previousPort = controls.port.echoField.input.value;
    if (platform === "windows") {
      controls.transport.echoField.input.value = "winrm";
      controls.transport.echoField.input.querySelector("option[value='ssh']").disabled = true;
    } else {
      controls.transport.echoField.input.querySelector("option[value='ssh']").disabled = false;
      if (transport === "winrm") controls.transport.echoField.input.value = "ssh";
    }
    const selectedTransport = controls.transport.echoField.input.value;
    const isWinRM = selectedTransport === "winrm";
    const isKey = selectedTransport === "ssh" && controls.authType.echoField.input.value === "key";
    if (!previousPort || previousPort === "22" || previousPort === "5986") {
      controls.port.echoField.input.value = isWinRM ? "5986" : "22";
    }
    controls.authType.hidden = isWinRM;
    controls.password.hidden = isKey;
    controls.keyPath.hidden = !isKey;
    controls.insecureTLS.hidden = !isWinRM;
  };
  controls.platform.echoField.input.addEventListener("change", syncConnectionFields);
  controls.transport.echoField.input.addEventListener("change", syncConnectionFields);
  controls.authType.echoField.input.addEventListener("change", syncConnectionFields);
  syncConnectionFields();

  async function saveDevice() {
    try {
      const port = Number(controls.port.echoField.input.value);
      if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error("连接端口必须在 1–65535 范围内。");
      const payload = {
        name: controls.name.echoField.input.value,
        address: controls.address.echoField.input.value,
        platform: controls.platform.echoField.input.value,
        transport: controls.transport.echoField.input.value,
        port,
        username: controls.username.echoField.input.value,
        authType: controls.authType.echoField.input.value,
        password: controls.password.echoField.input.value,
        keyPath: controls.keyPath.echoField.input.value,
        insecureTLS: controls.insecureTLS.echoControl.checked,
      };
      save.disabled = true;
      await api(editing ? `/api/devices/${device.id}` : "/api/devices", { method: editing ? "PUT" : "POST", body: JSON.stringify(payload) });
      state.deviceModal.echoModal.close();
      showFeedback(editing ? "设备配置已保存。" : "设备已添加。", "success");
      await refreshDevices(true);
      await refreshInstances(true);
    } catch (error) {
      showFeedback(error.message, "error");
    } finally {
      save.disabled = false;
    }
  }
}

async function testDevice(device, button) {
  button.disabled = true;
  try {
    await api(`/api/devices/${device.id}/test`, { method: "POST" });
    showFeedback(`${device.name} 连接成功。`, "success");
    await refreshDevices(true);
  } catch (error) {
    showFeedback(error.message, "error");
    await refreshDevices(true);
  } finally {
    button.disabled = false;
  }
}

function openInstanceEditor(instance = null) {
  state.modal?.echoModal?.destroy();
  const editing = Boolean(instance);
  const values = {
    name: instance?.name || "",
    deviceId: instance?.deviceId || "local",
    executablePath: instance?.executablePath || "",
    configuredPort: String(instance?.configuredPort || "8080"),
    arguments: (instance?.arguments || []).join(" "),
    environment: Object.entries(instance?.environment || {}).map(([key, value]) => `${key}=${value}`).join(" "),
  };
  const controls = {};

  const content = document.createElement("div");
  content.className = "em-form-content";
  const basicFields = document.createElement("div");
  basicFields.className = "em-form-grid";
  controls.name = field({ label: "显示名称", name: "name", placeholder: "服务名称", required: true, value: values.name });
  controls.device = selectField({ label: "运行设备", name: "deviceId", value: values.deviceId, options: deviceOptions(), hint: "本机直接运行；其他设备通过已配置的 SSH / WinRM 控制。" });
  controls.path = field({ label: "程序路径（绝对路径）", name: "executablePath", placeholder: "可执行文件的完整路径", required: true, value: values.executablePath, hint: "填写可执行文件本身，不是所在目录。" });
  controls.port = field({ label: "服务端口", name: "configuredPort", type: "number", value: values.configuredPort, attributes: { min: "1", max: "65535", inputmode: "numeric" } });
  controls.args = field({ label: "启动参数", name: "arguments", value: values.arguments, hint: "用空格分隔，如：--port 8080 --host 0.0.0.0" });
  controls.env = field({ label: "环境变量", name: "environment", value: values.environment, hint: "用空格或逗号分隔，格式为 KEY=value" });
  basicFields.append(controls.name, controls.device, controls.path, controls.port);
  const advanced = document.createElement("details");
  advanced.className = "em-advanced-settings";
  advanced.innerHTML = '<summary><span>高级设置</span><img src="/kit/SFSymbols/chevron.down.svg" alt="" aria-hidden="true"></summary>';
  const advancedFields = document.createElement("div");
  advancedFields.className = "em-form-grid em-advanced-settings-fields";
  advancedFields.append(controls.args, controls.env);
  advanced.append(advancedFields);
  content.append(basicFields, advanced);

  const cancel = makeTextAction({ label: "取消", icon: "xmark", color: "gray", onClick: () => state.modal.echoModal.close() });
  const save = makeTextAction({ label: editing ? "保存修改" : "添加实例", icon: "checkmark", color: "blue", onClick: () => saveInstance() });
  const actions = editing ? [cancel] : [cancel, save];
  if (editing) {
    let deleteAction;
    deleteAction = makeTextAction({
      label: "删除实例",
      icon: "trash",
      color: "red",
      className: "em-editor-delete-action",
      onClick: () => openDeleteConfirmation(instance),
    });
    actions.push(deleteAction, save);
  }

  state.modal = createEchoModal({
    id: "em-instance-modal",
    icon: "/static/assets/default-task-icon.png",
    title: editing ? "编辑服务实例" : "添加服务实例",
    addition: "管理程序启动配置，不会修改它自己的数据文件。",
    content,
    actions,
    className: editing ? "em-editor-modal em-editor-modal--editing" : "em-editor-modal",
    overlayClassName: "em-editor-overlay",
  });
  $("#em-modal-root").append(state.modal);
  state.modal.echoModal.open();
  if (instance) {
    const editorModal = state.modal;
    const taskIcon = editorModal.querySelector(".echo-modal__mark");
    if (taskIcon) {
      taskIcon.removeAttribute("aria-hidden");
      taskIcon.classList.add("em-modal-task-icon");
      installTaskIconPicker(taskIcon, instance);
    }
    loadTaskIcon(instance).then((iconURL) => {
      if (iconURL && state.modal === editorModal) {
        const image = editorModal.querySelector(".echo-modal__mark img");
        if (image) image.src = iconURL;
      }
    });
  }
  async function saveInstance() {
    try {
      const port = Number(controls.port.echoField.input.value);
      if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error("端口必须在 1–65535 范围内。");
      const payload = {
        name: controls.name.echoField.input.value,
        deviceId: controls.device.echoField.input.value === "local" ? "" : controls.device.echoField.input.value,
        executablePath: controls.path.echoField.input.value,
        configuredPort: port,
        arguments: parseArguments(controls.args.echoField.input.value),
        environment: parseEnvironment(controls.env.echoField.input.value),
      };
      save.disabled = true;
      await api(editing ? `/api/instances/${instance.id}` : "/api/instances", {
        method: editing ? "PUT" : "POST",
        body: JSON.stringify(payload),
      });
      state.modal.echoModal.close();
      showFeedback(editing ? "实例配置已保存。" : "实例已添加。", "success");
      await refreshInstances(true);
    } catch (error) {
      showFeedback(error.message, "error");
    } finally {
      save.disabled = false;
    }
  }
}

function makeTextAction({ label, icon, color, className = "", onClick }) {
  return createEchoButton({ label, icon, color, className, ariaLabel: label, onClick });
}

const tabs = createEchoTabs({
  id: "em-mode-switch",
  className: "em-mode-switch",
  ariaLabel: "EchoManager 页面",
  activeId: 0,
  items: [
    { icon: "waveform.path.ecg", iconActive: "waveform.path.ecg", ariaLabel: "实例", title: "实例" },
    { icon: "gear", iconActive: "gear", ariaLabel: "设置", title: "设置" },
  ],
  onChange: (index) => setView(index),
});
$(".em-mode-switch").replaceWith(tabs);

function settingsCopy(path) {
  return path.split(".").reduce((value, key) => value?.[key], SETTINGS_CONFIG.copy);
}

function applySettingsConfig() {
  document.querySelectorAll("[data-settings-copy]").forEach((element) => {
    const value = settingsCopy(element.dataset.settingsCopy);
    if (typeof value === "string") element.textContent = value;
  });
  document.querySelectorAll("[data-settings-aria-copy]").forEach((element) => {
    const value = settingsCopy(element.dataset.settingsAriaCopy);
    if (typeof value === "string") element.setAttribute("aria-label", value);
  });
  document.querySelectorAll("[data-settings-title-copy]").forEach((element) => {
    const value = settingsCopy(element.dataset.settingsTitleCopy);
    if (typeof value === "string") element.setAttribute("title", value);
  });
  document.querySelectorAll("[data-em-theme]").forEach((button) => {
    const option = SETTINGS_CONFIG.appearance.themeOptions.find(({ id }) => id === button.dataset.emTheme);
    if (option) button.textContent = option.label;
  });

}

function initializeSettingsPanels() {
  document.querySelectorAll(".em-settings-panel[data-settings-panel]").forEach((source) => {
    const key = source.dataset.settingsPanel;
    const header = source.querySelector(".em-settings-panel-head");
    const content = source.querySelector(".em-settings-panel-collapsible-content");
    if (!header || !content) return;
    const row = createEchoSettingRow({
      icon: SETTINGS_CONFIG.panels[key]?.icon || "slider.horizontal.3",
      title: header.querySelector(".em-settings-panel-head-copy h3")?.textContent.trim() || "",
      description: header.querySelector(".em-settings-panel-head-copy p")?.textContent.trim() || "",
      expanded: typeof state.settingsPanels[key] === "boolean" ? state.settingsPanels[key] : source.classList.contains("is-expanded"),
      content: [...content.children],
      onToggle: (expanded) => {
        state.settingsPanels[key] = expanded;
        persistSettingsPanelState();
      },
    });
    for (const className of source.classList) {
      if (className.startsWith("settings-panel-")) row.classList.add(className);
    }
    row.classList.add("em-settings-panel");
    row.dataset.settingsPanel = key;
    source.replaceWith(row);
  });
}

function setView(index) {
  state.currentView = index;
  $("#em-task-view").hidden = index !== 0;
  $("#em-settings-view").hidden = index !== 1;
  $("#em-add-action").hidden = index !== 0;
  const inTaskView = index === 0;
  $("#em-refresh-container").hidden = !inTaskView && index !== 1;
  $("#em-refresh-container").setAttribute("aria-label", inTaskView ? "刷新任务状态" : "关闭 EchoManager");
  refreshButton.hidden = !inTaskView;
  shutdownButton.hidden = index !== 1;
  if (index === 1) void refreshDevices(true);
}

function setTheme(preference) {
  const themeOptions = SETTINGS_CONFIG.appearance.themeOptions.map((option) => option.id);
  state.theme = themeOptions.includes(preference) ? preference : SETTINGS_CONFIG.defaults.theme;
  const resolved = state.theme === "system" ? (systemTheme.matches ? "dark" : "light") : state.theme;
  document.documentElement.dataset.theme = resolved;
  $("meta[name='theme-color']")?.setAttribute("content", SETTINGS_CONFIG.appearance.themeColors[resolved]);
  document.querySelectorAll("[data-em-theme]").forEach((button) => {
    button.setAttribute("aria-pressed", String(button.dataset.emTheme === state.theme));
  });
  try { localStorage.setItem(SETTINGS_CONFIG.storageKeys.theme, state.theme); } catch {}
}

function setAccentColor(value) {
  if (!/^#[0-9a-f]{6}$/i.test(value)) return;
  state.accentColor = value.toLowerCase();
  const root = document.documentElement;
  root.style.setProperty("--em-accent", state.accentColor);
  root.style.setProperty("--echo-color-accent", state.accentColor);
  root.style.setProperty("--echo-color-accent-hover", `color-mix(in srgb, ${state.accentColor} 84%, black)`);
  root.style.setProperty("--echo-color-accent-soft", `color-mix(in srgb, ${state.accentColor} 18%, var(--em-canvas))`);
  const picker = $("#em-accent-picker");
  if (picker) picker.value = state.accentColor;
  const description = $("#em-accent-description");
  if (description) description.textContent = state.accentColor === DEFAULT_ACCENT_COLOR
    ? SETTINGS_CONFIG.appearance.systemAccentDescription
    : SETTINGS_CONFIG.appearance.customAccentDescription;
  try { localStorage.setItem(ACCENT_PREFERENCE_KEY, state.accentColor); } catch {}
}

function initTheme() {
  let preference = SETTINGS_CONFIG.defaults.theme;
  try {
    const saved = localStorage.getItem(SETTINGS_CONFIG.storageKeys.theme);
    if (SETTINGS_CONFIG.appearance.themeOptions.some((option) => option.id === saved)) preference = saved;
  } catch {}
  setTheme(preference);
  setAccentColor(state.accentColor);
  systemTheme.addEventListener("change", () => {
    if (state.theme === "system") setTheme("system");
  });
  document.querySelectorAll("[data-em-theme]").forEach((button) => {
    button.addEventListener("click", () => setTheme(button.dataset.emTheme));
  });
  $("#em-accent-picker").addEventListener("input", (event) => setAccentColor(event.currentTarget.value));
}

function persistManagerPreferences() {
  try { localStorage.setItem(MANAGER_PREFERENCES_KEY, JSON.stringify(state.preferences)); } catch {}
}

function initManagerPreferences() {
  const confirmStop = $("#em-confirm-stop");
  confirmStop.checked = state.preferences.confirmStop;

  confirmStop.addEventListener("change", (event) => {
    state.preferences.confirmStop = event.currentTarget.checked;
    persistManagerPreferences();
  });
}

$("#em-add-action").append(makeIconButton({
  icon: "plus", label: "添加实例", color: "yellow", className: "em-float-action em-float-action--add", onClick: () => openInstanceEditor(),
}));

const refreshButton = makeIconButton({
  icon: "arrow.clockwise",
  label: "刷新任务状态",
  className: "em-float-action em-float-action--refresh",
  onClick: async (event) => {
    const button = event.currentTarget;
    if (state.refreshing) return;
    state.refreshing = true;
    button.disabled = true;
    button.classList.add("is-refreshing");
    try {
      const refreshed = await refreshInstances(false);
      if (refreshed) showFeedback("任务状态已刷新。", "success");
    } finally {
      state.refreshing = false;
      button.disabled = false;
      button.classList.remove("is-refreshing");
    }
  },
});
const shutdownButton = makeIconButton({
  icon: "power",
  label: "关闭 EchoManager",
  color: "red",
  className: "em-float-action em-float-action--shutdown",
  onClick: (event) => openManagerShutdownConfirmation(event.currentTarget),
});
shutdownButton.hidden = true;
$("#em-refresh-action").append(refreshButton, shutdownButton);

document.addEventListener("keydown", (event) => {
  if (event.key !== "Escape") return;
  if (state.cropModal?.classList.contains("is-open")) {
    event.preventDefault();
    event.stopImmediatePropagation();
    state.cropCleanup?.();
    return;
  }
  if (state.deleteModal?.classList.contains("is-open")) {
    event.preventDefault();
    event.stopImmediatePropagation();
    dismissDeleteConfirmation(true);
    return;
  }
  if (state.stopConfirmModal?.classList.contains("is-open")) {
    event.preventDefault();
    event.stopImmediatePropagation();
    dismissStopConfirmation();
    return;
  }
  if (state.logoutModal?.classList.contains("is-open")) {
    event.preventDefault();
    event.stopImmediatePropagation();
    dismissLogoutConfirmation();
    return;
  }
  if (state.managerShutdownModal?.classList.contains("is-open")) {
    event.preventDefault();
    event.stopImmediatePropagation();
    dismissManagerShutdownConfirmation();
    return;
  }
  state.modal?.echoModal?.close();
}, true);

const preventGestureZoom = (event) => event.preventDefault();
document.addEventListener("gesturestart", preventGestureZoom, { passive: false });
document.addEventListener("gesturechange", preventGestureZoom, { passive: false });
document.addEventListener("touchmove", (event) => {
  if (event.touches?.length > 1) event.preventDefault();
}, { passive: false });

applySettingsConfig();
initializeSettingsPanels();
initTheme();
initManagerPreferences();
$("#em-logout-button").addEventListener("click", (event) => openLogoutConfirmation(event.currentTarget));
api("/api/auth/session").then((user) => { state.currentUser = user.username; $("#em-current-user").textContent = user.username; }).catch(() => {});
refreshDevices(true);
refreshInstances();

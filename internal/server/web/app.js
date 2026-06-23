const state = {
  deviceId: localStorage.getItem("neondrop.deviceId") || generateDeviceId(),
  deviceName: localStorage.getItem("neondrop.deviceName") || "",
  deviceKind: localStorage.getItem("neondrop.deviceKind") || detectDeviceKind(),
  devices: [],
  transfers: [],
  selectedDeviceId: "",
  selectedFile: null,
  events: null,
  reconnectTimer: null,
};

function generateDeviceId() {
  if (globalThis.crypto?.randomUUID) {
    return globalThis.crypto.randomUUID().replaceAll("-", "");
  }

  const bytes = new Uint8Array(16);
  if (globalThis.crypto?.getRandomValues) {
    globalThis.crypto.getRandomValues(bytes);
  } else {
    for (let index = 0; index < bytes.length; index += 1) {
      bytes[index] = Math.floor(Math.random() * 256);
    }
  }
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

const elements = {
  deviceDialog: document.querySelector("#device-dialog"),
  deviceForm: document.querySelector("#device-form"),
  deviceNameInput: document.querySelector("#device-name-input"),
  deviceKindInput: document.querySelector("#device-kind-input"),
  cancelDevice: document.querySelector("#cancel-device"),
  editDevice: document.querySelector("#edit-device"),
  thisDevice: document.querySelector("#this-device"),
  deviceList: document.querySelector("#device-list"),
  deviceCount: document.querySelector("#device-count"),
  recipientName: document.querySelector("#recipient-name"),
  recipientDot: document.querySelector("#recipient-dot"),
  recipientState: document.querySelector("#recipient-state"),
  serverAddress: document.querySelector("#server-address"),
  copyAddress: document.querySelector("#copy-address"),
  messageText: document.querySelector("#message-text"),
  messageCount: document.querySelector("#message-count"),
  sendMessage: document.querySelector("#send-message"),
  tabs: document.querySelectorAll(".tab-button"),
  views: document.querySelectorAll(".tab-view"),
  fileInput: document.querySelector("#file-input"),
  dropZone: document.querySelector("#drop-zone"),
  selectedFile: document.querySelector("#selected-file"),
  selectedFileName: document.querySelector("#selected-file-name"),
  selectedFileSize: document.querySelector("#selected-file-size"),
  clearFile: document.querySelector("#clear-file"),
  sendFile: document.querySelector("#send-file"),
  uploadProgress: document.querySelector("#upload-progress"),
  progressBar: document.querySelector("#progress-bar"),
  progressValue: document.querySelector("#progress-value"),
  progressLabel: document.querySelector("#progress-label"),
  activityList: document.querySelector("#activity-list"),
  toastStack: document.querySelector("#toast-stack"),
};

localStorage.setItem("neondrop.deviceId", state.deviceId);
elements.serverAddress.textContent = location.origin;

function detectDeviceKind() {
  const ua = navigator.userAgent.toLowerCase();
  if (/ipad|tablet/.test(ua)) return "tablet";
  if (/android|iphone|mobile/.test(ua)) return "phone";
  if (/macintosh|windows|linux/.test(ua)) return "laptop";
  return "device";
}

function suggestedName() {
  const labels = {
    laptop: "Laptop",
    desktop: "Desktop",
    phone: "Phone",
    tablet: "Tablet",
    device: "Device",
  };
  return `${labels[state.deviceKind] || "Device"} ${state.deviceId.slice(0, 4).toUpperCase()}`;
}

async function register() {
  const response = await fetch("/api/devices/register", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    credentials: "same-origin",
    body: JSON.stringify({
      id: state.deviceId,
      name: state.deviceName,
      kind: state.deviceKind,
    }),
  });
  const payload = await response.json();
  if (!response.ok) throw new Error(payload.error || "Could not register device");

  renderThisDevice();
  connectEvents();
  await loadTransfers();
}

function connectEvents() {
  if (state.events) state.events.close();
  state.events = new EventSource("/api/events");
  state.events.onopen = () => {
    if (state.reconnectTimer) {
      window.clearTimeout(state.reconnectTimer);
      state.reconnectTimer = null;
    }
  };
  state.events.onmessage = ({ data }) => {
    const update = JSON.parse(data);
    if (update.type === "presence") {
      state.devices = update.devices || [];
      renderDevices();
      return;
    }
    if (update.transfer) {
      upsertTransfer(update.transfer);
      renderActivity();
      if (update.type === "message" || update.type === "file") {
        showIncoming(update.transfer);
      }
    }
  };
  state.events.onerror = () => {
    elements.recipientState.textContent = "Reconnecting…";
    state.events.close();
    scheduleReconnect();
  };
}

function scheduleReconnect() {
  if (state.reconnectTimer) return;
  state.reconnectTimer = window.setTimeout(async () => {
    state.reconnectTimer = null;
    try {
      await register();
    } catch (_) {
      scheduleReconnect();
    }
  }, 1200);
}

async function loadTransfers() {
  const response = await apiFetch("/api/transfers");
  if (!response.ok) return;
  const payload = await response.json();
  state.transfers = payload.transfers || [];
  renderActivity();
}

function renderThisDevice() {
  elements.thisDevice.innerHTML = `
    <span class="device-symbol">${deviceSymbol(state.deviceKind)}</span>
    <span>
      <strong>${escapeHTML(state.deviceName)}</strong>
      <small>This device · connected</small>
    </span>
  `;
}

function renderDevices() {
  const peers = state.devices.filter((device) => device.id !== state.deviceId);
  const onlineCount = peers.filter((device) => device.online).length;
  elements.deviceCount.textContent = `${onlineCount} online`;

  if (!peers.length) {
    elements.deviceList.innerHTML = `<div class="empty-devices">Open this address on another device connected to the same Wi-Fi.</div>`;
    clearRecipient();
    return;
  }

  elements.deviceList.innerHTML = peers.map((device) => `
    <button class="device-item ${device.id === state.selectedDeviceId ? "selected" : ""} ${device.online ? "" : "offline-device"}"
      type="button" role="option" aria-selected="${device.id === state.selectedDeviceId}" data-device-id="${device.id}">
      <span class="status-dot ${device.online ? "online" : "offline"}"></span>
      <span>
        <strong>${escapeHTML(device.name)}</strong>
        <small>${device.online ? "Available now" : `Last seen ${relativeTime(device.lastSeen)}`}</small>
      </span>
      <span class="device-kind">${escapeHTML(device.kind)}</span>
    </button>
  `).join("");

  elements.deviceList.querySelectorAll("[data-device-id]").forEach((button) => {
    button.addEventListener("click", () => {
      state.selectedDeviceId = button.dataset.deviceId;
      renderDevices();
      renderRecipient();
    });
  });

  if (state.selectedDeviceId && !peers.some((device) => device.id === state.selectedDeviceId)) {
    clearRecipient();
  } else {
    renderRecipient();
  }
}

function renderRecipient() {
  const recipient = selectedDevice();
  if (!recipient) {
    clearRecipient();
    return;
  }
  elements.recipientName.textContent = recipient.name;
  elements.recipientDot.className = `status-dot ${recipient.online ? "online" : "offline"}`;
  elements.recipientState.textContent = recipient.online ? "Ready to receive" : "Currently offline";
  updateActionState();
}

function clearRecipient() {
  state.selectedDeviceId = "";
  elements.recipientName.textContent = "Select a device";
  elements.recipientDot.className = "status-dot offline";
  elements.recipientState.textContent = "No recipient";
  updateActionState();
}

function selectedDevice() {
  return state.devices.find((device) => device.id === state.selectedDeviceId);
}

function updateActionState() {
  const recipient = selectedDevice();
  const canSend = Boolean(recipient && recipient.online);
  elements.sendMessage.disabled = !canSend || !elements.messageText.value.trim();
  elements.sendFile.disabled = !canSend || !state.selectedFile;
}

async function sendMessage() {
  const text = elements.messageText.value.trim();
  if (!text || !selectedDevice()?.online) return;

  elements.sendMessage.disabled = true;
  const response = await apiFetch("/api/messages", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ targetId: state.selectedDeviceId, text }),
  });
  const payload = await response.json();
  if (!response.ok) {
    toast("Could not send text", payload.error || "Request failed", "error");
  } else {
    upsertTransfer(payload);
    renderActivity();
    elements.messageText.value = "";
    elements.messageCount.textContent = "0 / 65,536";
    toast("Text sent", `Delivered to ${selectedDevice()?.name || "device"}`);
  }
  updateActionState();
}

function chooseFile(file) {
  if (!file) return;
  if (file.size > 512 * 1024 * 1024) {
    toast("File is too large", "The maximum transfer size is 512 MB.", "error");
    return;
  }
  state.selectedFile = file;
  elements.selectedFileName.textContent = file.name;
  elements.selectedFileSize.textContent = formatBytes(file.size);
  elements.selectedFile.classList.remove("hidden");
  updateActionState();
}

function clearFile() {
  state.selectedFile = null;
  elements.fileInput.value = "";
  elements.selectedFile.classList.add("hidden");
  updateActionState();
}

function sendFile() {
  if (!state.selectedFile || !selectedDevice()?.online) return;

  const targetName = selectedDevice().name;
  const data = new FormData();
  data.append("targetId", state.selectedDeviceId);
  data.append("file", state.selectedFile);

  const request = new XMLHttpRequest();
  request.open("POST", "/api/transfers");
  elements.uploadProgress.classList.remove("hidden");
  elements.progressLabel.textContent = "Uploading";
  elements.sendFile.disabled = true;

  request.upload.onprogress = (event) => {
    if (!event.lengthComputable) return;
    const percent = Math.round((event.loaded / event.total) * 100);
    elements.progressBar.value = percent;
    elements.progressValue.textContent = `${percent}%`;
  };
  request.onload = () => {
    let payload = {};
    try { payload = JSON.parse(request.responseText); } catch (_) {}
    if (request.status >= 200 && request.status < 300) {
      upsertTransfer(payload);
      renderActivity();
      toast("File sent", `${state.selectedFile.name} was sent to ${targetName}`);
      clearFile();
      elements.progressLabel.textContent = "Complete";
    } else {
      toast("Could not send file", payload.error || "Upload failed", "error");
      elements.progressLabel.textContent = "Upload failed";
    }
    window.setTimeout(() => elements.uploadProgress.classList.add("hidden"), 1200);
    updateActionState();
  };
  request.onerror = () => {
    toast("Network error", "The upload connection was interrupted.", "error");
    elements.progressLabel.textContent = "Upload failed";
    updateActionState();
  };
  request.send(data);
}

function renderActivity() {
  if (!state.transfers.length) {
    elements.activityList.innerHTML = `<div class="empty-activity">No transfers yet.</div>`;
    return;
  }

  elements.activityList.innerHTML = state.transfers.map((transfer) => {
    const incoming = transfer.to === state.deviceId;
    const peerId = incoming ? transfer.from : transfer.to;
    const peer = state.devices.find((device) => device.id === peerId);
    const peerName = peer?.name || "Unknown device";
    const title = transfer.kind === "file" ? transfer.filename : previewText(transfer.text);
    const detail = `${incoming ? "From" : "To"} ${peerName} · ${relativeTime(transfer.createdAt)}`;
    const actions = [];
    if (incoming && transfer.kind === "file") {
      actions.push(`<button class="small-button primary" data-download="${transfer.id}" type="button">Download</button>`);
    }
    if (incoming && transfer.kind === "message") {
      actions.push(`<button class="small-button primary" data-copy-message="${transfer.id}" type="button">Copy</button>`);
    }
    actions.push(`<button class="small-button" data-delete="${transfer.id}" type="button">Remove</button>`);
    return `
      <article class="activity-item">
        <span class="activity-icon">${transfer.kind === "file" ? "▤" : "⌘"}</span>
        <span class="activity-content">
          <strong>${escapeHTML(title)}</strong>
          <small>${escapeHTML(detail)}${transfer.receivedAt ? " · received" : ""}</small>
        </span>
        <span class="activity-actions">${actions.join("")}</span>
      </article>
    `;
  }).join("");

  elements.activityList.querySelectorAll("[data-download]").forEach((button) => {
    button.addEventListener("click", () => downloadTransfer(button.dataset.download));
  });
  elements.activityList.querySelectorAll("[data-copy-message]").forEach((button) => {
    button.addEventListener("click", () => copyMessage(button.dataset.copyMessage));
  });
  elements.activityList.querySelectorAll("[data-delete]").forEach((button) => {
    button.addEventListener("click", () => deleteTransfer(button.dataset.delete));
  });
}

async function downloadTransfer(id) {
  const transfer = state.transfers.find((item) => item.id === id);
  const response = await apiFetch(`/api/transfers/${encodeURIComponent(id)}/download`);
  if (!response.ok) {
    toast("Download failed", "The file is no longer available.", "error");
    return;
  }
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = transfer?.filename || "download";
  link.click();
  URL.revokeObjectURL(url);
  await markReceived(id);
}

async function copyMessage(id) {
  const transfer = state.transfers.find((item) => item.id === id);
  if (!transfer) return;
  await copyText(transfer.text);
  await markReceived(id);
  toast("Copied", "Text is ready to paste.");
}

async function markReceived(id) {
  const response = await apiFetch(`/api/transfers/${encodeURIComponent(id)}/received`, { method: "POST" });
  if (!response.ok) return;
  const updated = await response.json();
  upsertTransfer(updated);
  renderActivity();
}

async function deleteTransfer(id) {
  const response = await apiFetch(`/api/transfers/${encodeURIComponent(id)}`, { method: "DELETE" });
  if (!response.ok) return;
  state.transfers = state.transfers.filter((transfer) => transfer.id !== id);
  renderActivity();
}

function showIncoming(transfer) {
  const sender = state.devices.find((device) => device.id === transfer.from);
  const title = transfer.kind === "file" ? "Incoming file" : "Incoming text";
  const detail = transfer.kind === "file"
    ? `${transfer.filename} from ${sender?.name || "another device"}`
    : `Message from ${sender?.name || "another device"}`;
  toast(title, detail);
}

function upsertTransfer(transfer) {
  const index = state.transfers.findIndex((item) => item.id === transfer.id);
  if (index >= 0) {
    state.transfers[index] = transfer;
  } else {
    state.transfers.unshift(transfer);
  }
  state.transfers.sort((a, b) => new Date(b.createdAt) - new Date(a.createdAt));
}

function toast(title, detail, type = "success") {
  const item = document.createElement("div");
  item.className = "toast";
  item.innerHTML = `
    <span class="status-dot ${type === "error" ? "offline" : "online"}"></span>
    <span><strong>${escapeHTML(title)}</strong><small>${escapeHTML(detail)}</small></span>
    <button type="button" aria-label="Close notification">×</button>
  `;
  item.querySelector("button").addEventListener("click", () => item.remove());
  elements.toastStack.append(item);
  window.setTimeout(() => item.remove(), 5200);
}

async function apiFetch(path, options = {}) {
  return fetch(path, { ...options, credentials: "same-origin" });
}

async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text);
  } catch (_) {
    const input = document.createElement("textarea");
    input.value = text;
    input.style.position = "fixed";
    input.style.opacity = "0";
    document.body.append(input);
    input.select();
    document.execCommand("copy");
    input.remove();
  }
}

function openDeviceDialog(canCancel) {
  elements.deviceNameInput.value = state.deviceName || suggestedName();
  elements.deviceKindInput.value = state.deviceKind;
  elements.cancelDevice.classList.toggle("hidden", !canCancel);
  elements.deviceDialog.showModal();
  elements.deviceNameInput.focus();
  elements.deviceNameInput.select();
}

elements.deviceForm.addEventListener("submit", async (event) => {
  event.preventDefault();
  state.deviceName = elements.deviceNameInput.value.trim();
  state.deviceKind = elements.deviceKindInput.value;
  if (!state.deviceName) return;

  localStorage.setItem("neondrop.deviceName", state.deviceName);
  localStorage.setItem("neondrop.deviceKind", state.deviceKind);
  elements.deviceDialog.close();
  try {
    await register();
  } catch (error) {
    toast("Connection failed", error.message, "error");
    openDeviceDialog(false);
  }
});

elements.cancelDevice.addEventListener("click", () => elements.deviceDialog.close());
elements.editDevice.addEventListener("click", () => openDeviceDialog(true));
elements.copyAddress.addEventListener("click", async () => {
  await copyText(location.origin);
  toast("Address copied", "Open it on another device connected to this Wi-Fi.");
});

elements.messageText.addEventListener("input", () => {
  elements.messageCount.textContent = `${elements.messageText.value.length.toLocaleString()} / 65,536`;
  updateActionState();
});
elements.sendMessage.addEventListener("click", sendMessage);

elements.tabs.forEach((button) => {
  button.addEventListener("click", () => {
    elements.tabs.forEach((tab) => tab.classList.toggle("active", tab === button));
    elements.views.forEach((view) => view.classList.toggle("active", view.id === `tab-${button.dataset.tab}`));
  });
});

elements.fileInput.addEventListener("change", () => chooseFile(elements.fileInput.files[0]));
elements.clearFile.addEventListener("click", clearFile);
elements.sendFile.addEventListener("click", sendFile);

["dragenter", "dragover"].forEach((type) => elements.dropZone.addEventListener(type, (event) => {
  event.preventDefault();
  elements.dropZone.classList.add("dragging");
}));
["dragleave", "drop"].forEach((type) => elements.dropZone.addEventListener(type, (event) => {
  event.preventDefault();
  elements.dropZone.classList.remove("dragging");
}));
elements.dropZone.addEventListener("drop", (event) => chooseFile(event.dataTransfer.files[0]));

function deviceSymbol(kind) {
  return { laptop: "▱", desktop: "▣", phone: "▯", tablet: "▤", device: "◎" }[kind] || "◎";
}

function previewText(text) {
  const compact = String(text || "").replace(/\s+/g, " ").trim();
  return compact.length > 72 ? `${compact.slice(0, 72)}…` : compact;
}

function formatBytes(bytes) {
  if (!Number.isFinite(bytes) || bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB"];
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / 1024 ** index).toFixed(index ? 1 : 0)} ${units[index]}`;
}

function relativeTime(value) {
  const seconds = Math.max(0, Math.floor((Date.now() - new Date(value).getTime()) / 1000));
  if (seconds < 10) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

if (state.deviceName) {
  register().catch((error) => {
    toast("Connection failed", error.message, "error");
    openDeviceDialog(false);
  });
} else {
  openDeviceDialog(false);
}

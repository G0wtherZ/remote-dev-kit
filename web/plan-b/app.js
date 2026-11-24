"use strict";
const $ = (id) => document.getElementById(id);
const state = { devices: [], sessions: [], schedules: [] };
let terminal,
  fit,
  socket,
  current = "",
  refreshing = false;
let csrf="";
const initial = new URLSearchParams(location.hash.slice(1));
const loginToken = initial.get("token");
const initialDevice = initial.get("device");
$("project").value = initial.get("project") || "";
history.replaceState(null, "", location.pathname);
function message(text) {
  $("status").textContent = text;
}
async function api(path, options = {}) {
 const headers=new Headers(options.headers||{});if(csrf)headers.set("X-Rdev-CSRF",csrf);
 const response=await fetch(path,{credentials:"same-origin",...options,headers});const text=await response.text();let data;try{data=JSON.parse(text)}catch{data={error:text||response.statusText}};
 if(!response.ok)throw Error(data.error||`请求失败 ${response.status}`);if(data.csrf)csrf=data.csrf;return data;
}

function post(path, value) {
  return api(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(value),
  });
}
function option(value, text) {
  const node = document.createElement("option");
  node.value = value;
  node.textContent = text;
  return node;
}
async function refresh() {
  if (refreshing) return;
  refreshing = true;
  try {
    const data = await api("/api/state");
    Object.assign(state, data);
    if (initialDevice && !state.devices.some((d) => d.id === initialDevice))
      state.devices.push({
        id: initialDevice,
        platform: "当前电脑（未启用执行）",
      });
    const oldDevice = $("device").value || initialDevice;
    $("device").replaceChildren(
      ...state.devices.map((d) =>
        option(d.id, `${d.name || d.platform} · ${d.id.slice(0, 4)}`),
      ),
    );
    if (state.devices.some((d) => d.id === oldDevice))
      $("device").value = oldDevice;
    const oldSession = $("sessions").value || current;
    $("sessions").replaceChildren(
      option("", "选择已有会话"),
      ...state.sessions
        .sort((a, b) => b.created.localeCompare(a.created))
        .map((s) =>
          option(
            s.id,
            `${s.active ? "运行中" : "已结束"} · ${s.project} · ${new Date(s.created).toLocaleString()}`,
          ),
        ),
    );
    if (state.sessions.some((s) => s.id === oldSession))
      $("sessions").value = oldSession;
    renderSchedules();
    message("私网已连接");
  } catch (e) {
    message(e.message);
  } finally {
    refreshing = false;
  }
}
function project(kind = "claude") {
  return {
    device_id: $("device").value,
    project: $("project").value.trim(),
    kind,
  };
}
async function create(kind) {
  const body = project(kind);
  if (!body.device_id || !body.project) {
    message("请选择执行设备并填写本机项目路径");
    return;
  }
  message("正在新建远端终端…");
  const session = await post("/api/sessions", body);
  await refresh();
  $("sessions").value = session.id;
  page("terminal");
  attach(session.id);
}
function page(name) {
  for (const button of document.querySelectorAll("[data-page]"))
    button.setAttribute("aria-pressed", String(button.dataset.page === name));
  for (const id of ["terminal", "files", "schedules"])
    $(id + "-page").hidden = id !== name;
  if (name === "terminal") requestAnimationFrame(resize);
  if (name === "files" && !$("file-path").value)
    $("file-path").value = $("project").value;
}
for (const button of document.querySelectorAll("[data-page]"))
  button.addEventListener("click", () => page(button.dataset.page));
function resize() {
  if (!terminal || $("terminal-page").hidden) return;
  try {
    fit.fit();
    if (socket?.readyState === WebSocket.OPEN)
      socket.send(JSON.stringify({ rows: terminal.rows, cols: terminal.cols }));
  } catch {}
}
function attach(id) {
  if (!id) return;
  socket?.close();
  current = id;
  if (!terminal) {
    $("terminal").replaceChildren();
    terminal = new Terminal({
      fontSize: 15,
      fontFamily: "ui-monospace,SFMono-Regular,Consolas,monospace",
      cursorBlink: true,
      scrollback: 5000,
      screenReaderMode: true,
      theme: {
        background: "#171b1a",
        foreground: "#e7eee8",
        cursor: "#e7eee8",
        selectionBackground: "#3c5b44",
      },
    });
    fit = new FitAddon.FitAddon();
    terminal.loadAddon(fit);
    terminal.open($("terminal"));
    new ResizeObserver(() => requestAnimationFrame(resize)).observe(
      $("terminal"),
    );
    terminal.onData((data) => {
      if (socket?.readyState !== WebSocket.OPEN) return;
      const bytes = new TextEncoder().encode(data);
      if (bytes.length > 262144) {
        message("输入超过 256 KiB，请通过文件面板上传");
        return;
      }
      for (let i = 0; i < bytes.length; i += 16384)
        socket.send(bytes.subarray(i, i + 16384));
    });
  }
  terminal.reset();
  const next = new WebSocket(
    `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/api/terminal?session=${encodeURIComponent(id)}`, ["rdev-b-v1",csrf]
  );
  socket = next;
  next.binaryType = "arraybuffer";
  next.onopen = () => {
    resize();
    terminal.focus();
    message("已接回原生终端");
  };
  next.onmessage = (event) => {
    if (next !== socket) return;
    const bytes = new Uint8Array(event.data);
    terminal.write(bytes, () => {
      if (next.readyState === WebSocket.OPEN)
        next.send(JSON.stringify({ ack: bytes.byteLength }));
    });
  };
  next.onclose = () => {
    if (next === socket)
      message("终端连接已结束。运行中的会话可再次接回；输入不会自动重发。");
  };
  next.onerror = () => message("终端连接失败，请检查私网并接回终端。");
}
$("project-form").addEventListener("submit", (event) => {
  event.preventDefault();
  create("claude").catch((e) => message(e.message));
});
$("account").addEventListener("click", () =>
  create("account").catch((e) => message(e.message)),
);
$("sessions").addEventListener("change", () => attach($("sessions").value));
$("reconnect").addEventListener("click", () => attach($("sessions").value));
$("refresh").addEventListener("click", refresh);
function fileURL(path, download = false) {
  return (
    "/api/files?" +
    new URLSearchParams({
      device: $("device").value,
      path,
      ...(download ? { download: "1" } : {}),
    })
  );
}
async function listFiles() {
  const path = $("file-path").value.trim();
  if (!path) return;
  const files = await api(fileURL(path));
  $("files").replaceChildren();
  for (const file of files) {
    const row = document.createElement("tr");
    const name = document.createElement("td");
    const next = path.replace(/[\\/]$/, "") + "/" + file.name;
    if (file.directory) {
      const open = document.createElement("button");
      open.type = "button";
      open.textContent = file.name + "/";
      open.onclick = () => {
        $("file-path").value = next;
        listFiles().catch((e) => ($("file-status").textContent = e.message));
      };
      name.append(open);
    } else {
      name.textContent = file.name;
    }
    const size = document.createElement("td");
    size.textContent = file.directory ? "—" : `${file.size.toLocaleString()} B`;
    const modified = document.createElement("td");
    modified.textContent = new Date(file.modified).toLocaleString();
    const action = document.createElement("td");
    if (!file.directory) {
      const a = document.createElement("a");
      a.textContent = "下载";
      a.href = fileURL(next, true);
      a.download = file.name;
      action.append(a);
    }
    row.append(name, size, modified, action);
    $("files").append(row);
  }
  $("file-status").textContent = `${files.length} 项；上传时保留已有同名文件。`;
}
$("file-form").addEventListener("submit", (event) => {
  event.preventDefault();
  listFiles().catch((e) => ($("file-status").textContent = e.message));
});
$("parent").addEventListener("click", () => {
  const path = $("file-path").value.replace(/\\/g, "/").replace(/\/$/, "");
  $("file-path").value = path.slice(0, path.lastIndexOf("/")) || "/";
  listFiles().catch((e) => ($("file-status").textContent = e.message));
});
$("upload").addEventListener("change", async () => {
  const file = $("upload").files[0];
  if (!file) return;
  const form = new FormData();
  form.append("file", file);
  $("file-status").textContent = "正在上传…";
  try {
    await api(fileURL($("file-path").value), { method: "POST", body: form });
    await listFiles();
  } catch (e) {
    $("file-status").textContent = e.message;
  } finally {
    $("upload").value = "";
  }
});
function renderSchedules() {
  $("schedules").replaceChildren();
  for (const task of state.schedules) {
    const li = document.createElement("li");
    const description = document.createElement("p");
    description.textContent = task.prompt;
    const detail = document.createElement("p");
    detail.textContent = `${task.enabled ? "已启用" : "已停用"} · ${new Date(task.next).toLocaleString()} · ${task.project}`;
    const result = document.createElement("p");
    result.textContent = task.last_result || "尚未执行";
    const edit = document.createElement("button");
    edit.type = "button";
    edit.textContent = "编辑";
    edit.onclick = () => {
      $("schedule-id").value = task.id;
      $("device").value = task.device;
      $("project").value = task.project;
      $("prompt").value = task.prompt;
      const d = new Date(task.next);
      $("next").value = new Date(d - d.getTimezoneOffset() * 60000)
        .toISOString()
        .slice(0, 16);
      $("interval").value = task.interval_seconds;
      $("prompt").focus();
    };
    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.textContent = task.enabled ? "停用" : "启用";
    toggle.onclick = () =>
      post("/api/schedules", { ...task, enabled: !task.enabled })
        .then(refresh)
        .catch((e) => message(e.message));
    li.append(description, detail, result, edit, toggle);
    $("schedules").append(li);
  }
}
$("schedule-form").addEventListener("submit", async (event) => {
  event.preventDefault();
  try {
    const value = {
      id: $("schedule-id").value,
      device: $("device").value,
      project: $("project").value,
      prompt: $("prompt").value,
      next: new Date($("next").value).toISOString(),
      interval_seconds: Number($("interval").value),
      enabled: true,
    };
    await post("/api/schedules", value);
    $("schedule-id").value = "";
    message("任务已保存");
    await refresh();
  } catch (e) {
    message(e.message);
  }
});
$("logout").addEventListener("click", async () => {
  await post("/api/logout", {});
  socket?.close();
  message("已退出网页。重新运行 rdev plan-b web 可登录。");
});
(async () => {
  try {
    if (loginToken) await post("/api/login", { token: loginToken });
    await refresh();
    setInterval(refresh, 30000);
  } catch (e) {
    message(e.message);
  }
})();

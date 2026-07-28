// Polyseat interface.
//
// The daemon pushes a token whenever anything changes and this reloads the
// state. No polling, no diffing, no framework: the whole page is a handful of
// cards and rebuilding them is cheaper than tracking what moved.

const el = (id) => document.getElementById(id);

let state = null;
let openLogs = new Set();
let stream = null;

// A page that renders nothing is worse than one that says what broke. Without
// this, the first version of the seat cards threw halfway through and left an
// empty column that looked exactly like "no seats".
window.addEventListener("error", (event) => showError(event.message));

// ------------------------------------------------------------------ requests

async function api(method, path, body) {
  const response = await fetch(path, {
    method,
    headers: body ? { "Content-Type": "application/json" } : {},
    body: body ? JSON.stringify(body) : undefined,
  });

  const text = await response.text();
  const data = text ? JSON.parse(text) : {};

  if (response.status === 401) {
    const err = new Error(data.error || "not logged in");
    err.unauthorized = true;
    throw err;
  }

  if (!response.ok) throw new Error(data.error || response.statusText);

  return data;
}

async function refresh() {
  try {
    state = await api("GET", "/api/state");
    render();
  } catch (err) {
    if (err.unauthorized) {
      showLogin();
      return;
    }

    // Swallowing this is how the empty column happened: the fetch succeeded,
    // rendering threw, and the catch turned a broken page into a silent one.
    console.error(err);
    showError(err.message || String(err));
  }
}

function showError(message) {
  const box = el("warnings");
  if (!box) return;

  const div = document.createElement("div");
  div.className = "warning";
  div.textContent = "The interface hit an error: " + message;
  box.prepend(div);
}

// -------------------------------------------------------------------- render

function render() {
  if (!state) return;

  el("hostname").textContent = state.host.hostname;

  const observer = el("observer");
  observer.hidden = false;
  observer.textContent = "uhid observer: " + state.observer;
  observer.className = "pill " + state.observer;

  // The list is absent rather than empty when there is nothing to warn about,
  // and calling map on that is what silently broke the whole page once.
  el("warnings").replaceChildren(
    ...(state.warnings || []).map((text) => {
      const div = document.createElement("div");
      div.className = "warning";
      div.textContent = text;
      return div;
    }),
  );

  const seats = el("seats");

  if (state.seats.length === 0) {
    seats.replaceChildren(
      Object.assign(document.createElement("p"), {
        className: "empty",
        textContent:
          "No seats yet. A seat is a container with its own session, its own " +
          "Sunshine and its own Steam account.",
      }),
    );
    return;
  }

  seats.replaceChildren(...state.seats.map(card));
}

function card(seat) {
  const node = document.createElement("section");
  node.className = "seat";

  const head = document.createElement("header");
  const title = document.createElement("h2");
  title.textContent = seat.label || seat.name;

  const name = document.createElement("span");
  name.className = "name";
  name.textContent = seat.name;

  const spacer = document.createElement("div");
  spacer.className = "spacer";

  const status = document.createElement("span");
  status.className = "pill " + seat.state;
  status.textContent = seat.busy || seat.state;

  head.append(title, name, spacer, status);
  node.append(head, facts(seat), actions(seat), logPanel(seat));

  return node;
}

function facts(seat) {
  const list = document.createElement("dl");
  list.className = "facts";

  const row = (label, value, className) => {
    if (value === null || value === undefined || value === "") return;

    const dt = document.createElement("dt");
    dt.textContent = label;

    const dd = document.createElement("dd");

    if (value instanceof Node) dd.append(value);
    else dd.textContent = value;

    if (className) dd.className = className;

    list.append(dt, dd);
  };

  const addresses = Object.entries(seat.addresses || {})
    .filter(([iface]) => iface === "eth1")
    .flatMap(([, list]) => list);

  if (addresses.length > 0) {
    const links = document.createElement("span");

    addresses.forEach((address, i) => {
      if (i > 0) links.append(", ");

      const link = document.createElement("a");
      link.href = `https://${address}:47990`;
      link.target = "_blank";
      link.rel = "noreferrer";
      link.textContent = address;
      links.append(link);
    });

    row("Address", links);
  } else if (seat.address) {
    row("Address", seat.address + " (configured)");
  }

  row("Session", `sway ${seat.sway || "?"}, sunshine ${seat.sunshine || "?"}`);

  // The one line worth staring at. A seat that fell back to software encoding
  // starts, streams and looks entirely healthy until somebody plays on it.
  if (seat.encoder) {
    row(
      "Encoder",
      seat.encoder === "libx264" ? seat.encoder + "  (software, the GPU path is broken)" : seat.encoder,
      seat.encoder === "libx264" ? "flag bad" : null,
    );
  }

  row("Input broker", seat.broker, seat.broker === "running" ? null : "flag");
  row("Devices", (seat.devices || []).join(", ") || "none attached");
  row("Resolution", seat.resolution);

  if (seat.stale) {
    row("Provisioning", "out of date, provision this seat again", "flag");
  }

  if (seat.error) row("Last error", seat.error, "flag bad");

  return list;
}

function actions(seat) {
  const bar = document.createElement("div");
  bar.className = "actions";

  const button = (label, handler, className) => {
    const b = document.createElement("button");
    b.textContent = label;
    if (className) b.className = className;
    b.disabled = Boolean(seat.busy);
    b.onclick = () => run(handler);
    bar.append(b);

    return b;
  };

  const running = seat.state === "running" || seat.state === "starting";

  if (running) button("Stop", () => api("POST", `/api/seats/${seat.name}/stop`));
  else button("Start", () => api("POST", `/api/seats/${seat.name}/start`), "primary");

  button(seat.state === "absent" ? "Build" : "Provision", () =>
    api("POST", `/api/seats/${seat.name}/provision`),
  );

  button("Edit", () => {
    openEditor(seat);
    return Promise.resolve();
  }).disabled = false;

  const remove = button(
    "Delete",
    () => {
      const keep = !confirm(
        `Delete the seat "${seat.name}" and its container, including everything ` +
          `installed in it?\n\nCancel keeps the container and only removes the seat.`,
      );

      return api("DELETE", `/api/seats/${seat.name}?keep_container=${keep ? 1 : 0}`);
    },
    "danger",
  );
  remove.disabled = Boolean(seat.busy);

  if (seat.busy) {
    const cancel = document.createElement("button");
    cancel.textContent = "Cancel";
    cancel.onclick = () => run(() => api("POST", `/api/seats/${seat.name}/cancel`));
    bar.append(cancel);
  }

  return bar;
}

function logPanel(seat) {
  const details = document.createElement("details");
  details.className = "log";
  details.open = openLogs.has(seat.name);

  const summary = document.createElement("summary");
  summary.textContent = "Log";

  const pre = document.createElement("pre");
  pre.className = "log";
  pre.textContent = "";

  details.append(summary, pre);

  details.ontoggle = () => {
    if (details.open) {
      openLogs.add(seat.name);
      loadLog(seat.name, pre);
    } else {
      openLogs.delete(seat.name);
    }
  };

  if (details.open) loadLog(seat.name, pre);

  return details;
}

async function loadLog(name, pre) {
  try {
    const data = await api("GET", `/api/seats/${name}/log`);
    pre.textContent = data.lines.join("\n") || "nothing yet";
    pre.scrollTop = pre.scrollHeight;
  } catch (err) {
    pre.textContent = String(err);
  }
}

async function run(handler) {
  try {
    await handler();
    await refresh();
  } catch (err) {
    if (err.unauthorized) {
      showLogin();
      return;
    }

    alert(err.message);
  }
}

// --------------------------------------------------------------------- login

function showLogin() {
  if (stream) {
    stream.close();
    stream = null;
  }

  el("app").hidden = true;
  el("account").hidden = true;
  el("login").hidden = false;
  el("hostname").textContent = "";
  el("observer").hidden = true;
  el("link").textContent = "signed out";
  el("link").className = "pill offline";
  el("login-form").username.focus();
}

function showApp() {
  el("login").hidden = true;
  el("app").hidden = false;
  el("account").hidden = false;
  el("login-form").password.value = "";
  el("login-error").textContent = "";

  refresh();
  connect();
}

async function submitLogin(event) {
  event.preventDefault();

  const form = el("login-form");
  const button = el("login-submit");

  // Verifying a password is deliberately slow, so say something rather than
  // letting the page look frozen for a second.
  button.disabled = true;
  el("login-error").textContent = "";

  try {
    await api("POST", "/api/login", {
      username: form.username.value.trim(),
      password: form.password.value,
    });

    showApp();
  } catch (err) {
    el("login-error").textContent = err.message;
    form.password.value = "";
    form.password.focus();
  } finally {
    button.disabled = false;
  }
}

async function submitPassword(event) {
  event.preventDefault();

  const form = el("password-form");
  el("password-error").textContent = "";

  try {
    await api("POST", "/api/password", {
      username: form.username.value.trim(),
      current: form.current.value,
      new: form.new.value,
    });

    el("password").close();
    form.current.value = "";
    form.new.value = "";
  } catch (err) {
    el("password-error").textContent = err.message;
  }
}

async function signOut() {
  try {
    await api("POST", "/api/logout");
  } catch (err) {
    console.error(err);
  }

  el("password").close();
  showLogin();
}

async function openAccount() {
  const session = await api("GET", "/api/session");
  const form = el("password-form");

  form.username.value = session.username;
  form.current.value = "";
  form.new.value = "";
  el("password-error").textContent = "";
  el("password").showModal();
}

// -------------------------------------------------------------------- editor

let editing = null;

function openEditor(seat) {
  editing = seat;

  const form = el("editor-form");
  el("editor-title").textContent = seat ? `Seat ${seat.name}` : "New seat";
  el("editor-error").textContent = "";

  form.name.value = seat ? seat.name : "";
  form.name.disabled = Boolean(seat);
  form.label.value = seat ? seat.label : "";
  form.resolution.value = seat ? seat.resolution : "1920x1080@60Hz";
  form.address.value = seat ? seat.address || "" : "";
  form.gateway.value = seat ? seat.gateway || "" : "";
  form.autostart.checked = seat ? seat.autostart : true;

  el("editor").showModal();
}

async function saveEditor(event) {
  event.preventDefault();

  const form = el("editor-form");
  const body = {
    label: form.label.value.trim(),
    resolution: form.resolution.value.trim(),
    address: form.address.value.trim(),
    gateway: form.gateway.value.trim(),
    autostart: form.autostart.checked,
  };

  try {
    if (editing) {
      await api("PATCH", `/api/seats/${editing.name}`, body);
    } else {
      body.name = form.name.value.trim();
      if (!body.label) body.label = body.name;
      await api("POST", "/api/seats", body);
    }

    el("editor").close();
    await refresh();
  } catch (err) {
    el("editor-error").textContent = err.message;
  }
}

// --------------------------------------------------------------------- setup

function connect() {
  if (stream) stream.close();

  const source = new EventSource("/api/events");
  stream = source;

  source.addEventListener("hello", () => {
    el("link").textContent = "live";
    el("link").className = "pill online";
    refresh();
  });

  source.addEventListener("change", refresh);

  source.onerror = () => {
    // A dropped stream is also how an expired session shows up here, since an
    // EventSource cannot report the 401 itself. Asking the API settles which
    // of the two it is.
    el("link").textContent = "reconnecting";
    el("link").className = "pill offline";

    api("GET", "/api/session")
      .then((session) => {
        if (!session.authenticated) {
          source.close();
          showLogin();
        }
      })
      .catch(() => {});
  };
}

el("add").onclick = () => openEditor(null);
el("editor-cancel").onclick = () => el("editor").close();
el("editor-form").onsubmit = saveEditor;
el("login-form").onsubmit = submitLogin;
el("password-form").onsubmit = submitPassword;
el("password-cancel").onclick = () => el("password").close();
el("logout").onclick = signOut;
el("account").onclick = () => run(openAccount);

// Ask before drawing anything, so a signed out visitor gets the login form
// rather than a flash of an empty seat list.
api("GET", "/api/session")
  .then((session) => (session.authenticated ? showApp() : showLogin()))
  .catch(() => showLogin());

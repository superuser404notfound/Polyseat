// Polyseat interface.
//
// The daemon pushes a token whenever anything changes and this reloads the
// state. No polling, no diffing, no framework: the whole page is a handful of
// cards and rebuilding them is cheaper than tracking what moved.

const el = (id) => document.getElementById(id);

let state = null;
let library = null;
let openLogs = new Set();
let openPairing = new Set();
let openSoftware = new Set();
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
    // Fetched together, because a seat card and the library view disagreeing
    // about which seats exist looks like a bug in whichever one you read second.
    const [next, pool] = await Promise.all([
      api("GET", "/api/state"),
      api("GET", "/api/library"),
    ]);

    state = next;
    library = pool;
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
  renderLibrary();
}

// ------------------------------------------------------------------- library

function renderLibrary() {
  const body = el("library-body");
  const saving = el("library-saving");

  el("library-import").disabled = !library || !library.available;
  el("library-sync").disabled = !library || !library.available;

  if (!library || !library.available) {
    saving.hidden = true;

    // Saying why rather than hiding the section. "My games are not being
    // shared" is otherwise a silent failure with nowhere to look.
    body.replaceChildren(
      Object.assign(document.createElement("p"), {
        className: "empty",
        textContent:
          "The shared library is off. " +
          ((library && library.problem) ||
            "The daemon did not report a reason.") +
          " Sharing games without copying them needs a filesystem that can " +
          "share blocks between files: btrfs, or XFS created with reflink=1. " +
          "ext4 cannot do it.",
      }),
    );
    return;
  }

  saving.hidden = false;
  // Phrased as sharing rather than as a download saved, because a title in the
  // pool that only one seat has was not downloaded twice either. What the
  // number says is how much the seats' copies would have cost as real copies.
  saving.textContent = library.saved
    ? bytes(library.saved) + " shared instead of copied"
    : "nothing shared yet";
  saving.className = "pill " + (library.saved ? "online" : "");

  const outside = library.outside || [];

  // Named rather than left out. A seat missing from the pool looks exactly like
  // a broken pool, and taking part is a per seat setting that is off by default
  // because it is the only thing that mounts host storage into a seat.
  const aside =
    outside.length === 0
      ? null
      : Object.assign(document.createElement("p"), {
          className: "hint",
          textContent:
            (outside.length === 1 ? "Seat " : "Seats ") +
            outside.join(", ") +
            (outside.length === 1 ? " does" : " do") +
            " not take part. Turn it on with Edit on the seat, tick " +
            "\u201cTake part in the shared game library\u201d and save.",
        });

  const titles = library.titles || [];

  if (titles.length === 0) {
    body.replaceChildren(
      ...[
        Object.assign(document.createElement("p"), {
          className: "empty",
          textContent:
            "Nothing in the pool yet. Install a game in any seat that takes part " +
            "and it appears here within a minute, then in the other seats. " +
            "Games already on this machine can be brought in with Import.",
        }),
        aside,
      ].filter(Boolean),
    );
    return;
  }

  const table = document.createElement("table");
  table.className = "titles";

  const head = document.createElement("thead");
  head.innerHTML =
    "<tr><th>Title</th><th>Size</th><th>In</th><th></th></tr>";

  const rows = document.createElement("tbody");

  titles.forEach((title) => rows.append(titleRow(title)));

  table.append(head, rows);

  const sources = library.sources || [];

  const tracking = sources.length
    ? Object.assign(document.createElement("p"), {
        className: "hint",
        textContent:
          "Also watching " +
          sources.join(", ") +
          ", so a game updated there reaches the seats by itself.",
      })
    : null;

  const note = document.createElement("p");
  note.className = "hint";
  note.textContent =
    "The pool holds " +
    bytes(library.bytes) +
    " in " +
    library.root +
    ". Copies in the seats share those blocks, so they cost almost nothing " +
    "until a seat updates a game. Sharing files is not sharing licences: a " +
    "seat can only play what its own Steam account owns.";

  body.replaceChildren(...[table, note, tracking, aside].filter(Boolean));
}

function titleRow(title) {
  const row = document.createElement("tr");

  const name = document.createElement("td");
  name.textContent = title.name || title.installdir || title.appid;

  // Where it came from. Both kinds sit in one list because from where somebody
  // is standing they are both just games in the pool, but a Steam title behaves
  // differently from a plain folder and the badge is where that shows.
  if (title.kind === "folder") {
    const badge = document.createElement("span");
    badge.className = "badge";
    badge.textContent = "folder";
    badge.title =
      "Shared as a plain directory, for launchers other than Steam. " +
      "Nothing tells Polyseat when such an install has finished, so it waits " +
      "until the folder stops changing.";
    name.append(" ", badge);
  }

  const size = document.createElement("td");
  size.className = "num";
  size.textContent = bytes(title.bytes);

  const where = document.createElement("td");
  const inSeats = title.in || [];
  const declined = title.declined || [];

  const stale = title.stale || [];

  where.append(
    Object.assign(document.createElement("span"), {
      textContent: inSeats.length ? inSeats.join(", ") : "no seat",
      className: inSeats.length ? "" : "flag",
    }),
  );

  // A seat one build behind is not a failure, it is a seat that was busy when
  // the update came through. Saying so beats leaving somebody to wonder why one
  // seat is a patch back.
  if (stale.length) {
    const waiting = document.createElement("span");
    waiting.className = "flag";
    waiting.textContent = " (update waiting for " + stale.join(", ") + ")";
    waiting.title =
      "The pool has a newer build. It is applied as soon as nothing in that " +
      "seat is using the shared library, because replacing files under a " +
      "running game would corrupt the install.";
    where.append(waiting);
  }

  // A seat that turned a title down is shown with a way to offer it again,
  // rather than looking like the sharing quietly failed for that seat.
  declined.forEach((seat) => {
    const button = document.createElement("button");
    button.className = "quiet tiny";
    button.textContent = "+ " + seat;
    button.title =
      seat + " uninstalled this, so it is not offered again. Click to send it back.";
    button.onclick = () =>
      run(() => api("POST", `/api/library/${title.appid}/offer/${seat}`));
    where.append(" ", button);
  });

  const actions = document.createElement("td");
  actions.className = "right";

  const remove = document.createElement("button");
  remove.className = "quiet tiny danger";
  remove.textContent = "Remove";
  remove.title =
    "Removes this title from the pool. The seats that have it keep it.";
  remove.onclick = () => {
    if (
      !confirm(
        `Remove "${title.name}" from the shared pool?\n\n` +
          `The seats that already have it keep their copies, so this frees ` +
          `nothing until the last of them uninstalls it. It will stop being ` +
          `offered to new seats.`,
      )
    ) {
      return;
    }

    run(() => api("DELETE", `/api/library/${title.appid}`));
  };

  actions.append(remove);
  row.append(name, size, where, actions);

  return row;
}

function bytes(n) {
  if (!n) return "0 B";

  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;

  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }

  return (i === 0 ? n : n.toFixed(1)) + " " + units[i];
}

// The daemon lists the Steam libraries it can find on the host, so importing is
// usually a click. Typing a path stays possible for anything on another disk.
function openImport() {
  const form = el("import-form");
  const list = el("import-candidates");
  const found = (library && library.candidates) || [];

  el("import-error").textContent = "";
  form.path.value = found[0] || "";

  list.replaceChildren(
    ...found.map((path) => {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "quiet tiny";
      button.textContent = path;
      button.onclick = () => (form.path.value = path);
      return button;
    }),
  );

  list.hidden = found.length === 0;
  el("import").showModal();
}

async function submitImport(event) {
  event.preventDefault();

  const form = el("import-form");
  const button = el("import-submit");

  el("import-error").textContent = "";
  button.disabled = true;
  button.textContent = "Importing";

  try {
    const report = await api("POST", "/api/library/import", {
      path: form.path.value.trim(),
    });

    el("import").close();

    const taken = (report.harvested || []).length;
    alert(
      (taken === 0
        ? "Now watching that library. Everything installed there is already in the pool."
        : `Now watching that library, and took ${taken} title${taken === 1 ? "" : "s"} from it.`) +
        " Anything updated there from now on reaches the seats by itself." +
        (report.problems && report.problems.length
          ? "\n\n" + report.problems.join("\n")
          : ""),
    );

    await refresh();
  } catch (err) {
    el("import-error").textContent = err.message;
  } finally {
    button.disabled = false;
    button.textContent = "Watch";
  }
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
  node.append(
    head,
    facts(seat),
    actions(seat),
    softwarePanel(seat),
    pairingPanel(seat),
    logPanel(seat),
  );

  return node;
}

// What is installed in a seat, and how to change it.
//
// Here rather than in a dialog because it is a property of one seat, and
// loaded only when opened for the same reason pairing is: it asks the
// container, and doing that for every seat on every state change would be a
// steady stream of exec calls into machines that are supposed to be busy
// playing games.
function softwarePanel(seat) {
  const details = document.createElement("details");
  details.className = "log";
  details.open = openSoftware.has(seat.name);

  const summary = document.createElement("summary");
  summary.textContent = "Software";

  const body = document.createElement("div");
  body.className = "software";

  details.append(summary, body);

  details.ontoggle = () => {
    if (details.open) {
      openSoftware.add(seat.name);
      loadSoftware(seat, body);
    } else {
      openSoftware.delete(seat.name);
    }
  };

  if (details.open) loadSoftware(seat, body);

  return details;
}

async function loadSoftware(seat, body) {
  body.replaceChildren(note("loading"));

  let status;

  try {
    status = await api("GET", `/api/seats/${seat.name}/software`);
  } catch (err) {
    if (err.unauthorized) return showLogin();

    body.replaceChildren(note(err.message));

    return;
  }

  body.replaceChildren();

  if (!status.available) {
    body.append(
      note(status.problem || "software cannot be managed in this seat right now"),
    );

    return;
  }

  const installed = status.installed || [];
  const have = new Set(installed.map((app) => app.id));

  body.append(
    note(
      "Installed for the player, without root. Anything here that Polyseat " +
        "recognises also turns up in the Moonlight app list.",
    ),
  );

  if (installed.length === 0) {
    body.append(note("Nothing is installed in this seat yet."));
  } else {
    const list = document.createElement("ul");
    list.className = "devices";

    installed.forEach((app) => {
      const item = document.createElement("li");

      const name = document.createElement("span");
      name.textContent = app.size ? `${app.name} (${app.size})` : app.name;
      name.title = app.id;

      const remove = document.createElement("button");
      remove.textContent = "Remove";
      remove.className = "danger";
      remove.onclick = () =>
        run(async () => {
          await api("DELETE", `/api/seats/${seat.name}/software/${app.id}`);
          await loadSoftware(seat, body);
        });

      item.append(name, remove);
      list.append(item);
    });

    body.append(list);
  }

  const offer = (status.catalog || []).filter((entry) => !have.has(entry.id));

  if (offer.length > 0) {
    const grid = document.createElement("ul");
    grid.className = "devices";

    offer.forEach((entry) => {
      const item = document.createElement("li");

      const name = document.createElement("span");
      name.textContent = `${entry.name} - ${entry.summary}`;
      name.title = entry.id;

      const add = document.createElement("button");
      add.textContent = "Install";
      add.onclick = () =>
        run(async () => {
          await api("POST", `/api/seats/${seat.name}/software`, { id: entry.id });
          await loadSoftware(seat, body);
        });

      item.append(name, add);
      grid.append(item);
    });

    body.append(grid);
  }

  body.append(installForm(seat, body));
}

// Anything on Flathub, by id. The catalog above is a shortcut, not a fence.
function installForm(seat, body) {
  const form = document.createElement("form");
  form.className = "pair";

  const id = document.createElement("input");
  id.placeholder = "Any Flathub id, for example org.videolan.VLC";
  id.autocomplete = "off";
  id.spellcheck = false;
  id.required = true;

  const submit = document.createElement("button");
  submit.className = "primary";
  submit.textContent = "Install";

  form.append(id, submit);

  form.onsubmit = (event) => {
    event.preventDefault();
    submit.disabled = true;

    run(async () => {
      try {
        await api("POST", `/api/seats/${seat.name}/software`, {
          id: id.value.trim(),
        });
        await loadSoftware(seat, body);
      } finally {
        submit.disabled = false;
      }
    });
  };

  return form;
}

// Pairing, in one place instead of one Sunshine page per seat.
//
// Loaded when the panel is opened rather than with the rest of the state: each
// of these is a call into that seat's Sunshine, and doing three of them every
// time anything changes would be rude to something that is busy encoding video.
function pairingPanel(seat) {
  const details = document.createElement("details");
  details.className = "log";
  details.open = openPairing.has(seat.name);

  const summary = document.createElement("summary");
  summary.textContent = "Devices and pairing";

  const body = document.createElement("div");
  body.className = "pairing";

  details.append(summary, body);

  details.ontoggle = () => {
    if (details.open) {
      openPairing.add(seat.name);
      loadPairing(seat, body);
    } else {
      openPairing.delete(seat.name);
    }
  };

  if (details.open) loadPairing(seat, body);

  return details;
}

async function loadPairing(seat, body) {
  body.replaceChildren(note("loading"));

  if (seat.state !== "running") {
    body.replaceChildren(
      note("The seat has to be running before a device can be paired with it."),
    );
    return;
  }

  try {
    const [clients, access] = await Promise.all([
      api("GET", `/api/seats/${seat.name}/clients`),
      api("GET", `/api/seats/${seat.name}/sunshine`),
    ]);

    body.replaceChildren(
      deviceList(seat, clients.devices, body),
      pairForm(seat, body),
      sunshineAccess(access),
    );
  } catch (err) {
    if (err.unauthorized) {
      showLogin();
      return;
    }

    body.replaceChildren(note(err.message));
  }
}

function note(text) {
  const p = document.createElement("p");
  p.className = "note";
  p.textContent = text;
  return p;
}

function deviceList(seat, devices, body) {
  const wrap = document.createElement("div");

  if (!devices || devices.length === 0) {
    wrap.append(note("No device is paired with this seat yet."));
    return wrap;
  }

  const list = document.createElement("ul");
  list.className = "devices";

  devices.forEach((device) => {
    const item = document.createElement("li");

    const name = document.createElement("span");
    name.textContent = device.name || "unnamed";

    const remove = document.createElement("button");
    remove.textContent = "Unpair";
    remove.className = "danger";
    remove.onclick = () =>
      run(async () => {
        await api("POST", `/api/seats/${seat.name}/unpair`, { uuid: device.uuid });
        await loadPairing(seat, body);
      });

    item.append(name, remove);
    list.append(item);
  });

  wrap.append(list);

  return wrap;
}

function pairForm(seat, body) {
  const form = document.createElement("form");
  form.className = "pair";

  const pin = document.createElement("input");
  pin.placeholder = "PIN";
  pin.inputMode = "numeric";
  pin.autocomplete = "off";
  pin.required = true;

  const label = document.createElement("input");
  label.placeholder = "Name of the device";
  label.autocomplete = "off";

  const submit = document.createElement("button");
  submit.className = "primary";
  submit.textContent = "Pair";

  form.append(pin, label, submit);

  form.onsubmit = (event) => {
    event.preventDefault();
    submit.disabled = true;

    run(async () => {
      try {
        await api("POST", `/api/seats/${seat.name}/pair`, {
          pin: pin.value.trim(),
          name: label.value.trim(),
        });
        await loadPairing(seat, body);
      } finally {
        submit.disabled = false;
      }
    });
  };

  return form;
}

function sunshineAccess(access) {
  const wrap = document.createElement("p");
  wrap.className = "note";

  wrap.append("Moonlight shows the PIN. This seat's own Sunshine page is at ");

  if (access.url) {
    const link = document.createElement("a");
    link.href = access.url;
    link.target = "_blank";
    link.rel = "noreferrer";
    link.textContent = access.url;
    wrap.append(link);
  } else {
    wrap.append("its LAN address");
  }

  wrap.append(", sign in there with ");

  const creds = document.createElement("code");
  creds.textContent = `${access.username} / ${access.password}`;
  wrap.append(creds, ".");

  return wrap;
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
  row("Shared library", seat.library ? "yes" : "no");

  if (seat.stale) {
    row("Provisioning", "out of date, provision this seat again", "flag");
  }

  (seat.notes || []).forEach((note) => row("Note", note, "flag"));

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
  form.library.checked = seat ? seat.library : true;

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
    library: form.library.checked,
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
// Says what it did. A button that silently changes nothing is indistinguishable
// from a button that is broken, which is exactly how the per seat setting above
// managed to hide.
el("library-sync").onclick = () =>
  run(async () => {
    const before = JSON.stringify((library && library.titles) || []);
    const after = await api("POST", "/api/library/sync");

    if (JSON.stringify(after.titles || []) !== before) return;

    const outside = after.outside || [];
    const members = (after.titles || []).some((t) => (t.in || []).length);

    if (!members && outside.length) {
      alert(
        "Nothing changed. " +
          (outside.length === 1 ? "Seat " : "Seats ") +
          outside.join(", ") +
          (outside.length === 1 ? " does" : " do") +
          " not take part in the shared library yet. Turn it on with Edit on " +
          "the seat.",
      );
      return;
    }

    alert("Nothing to do: every seat that takes part already has everything in the pool.");
  });
el("library-import").onclick = openImport;
el("import-cancel").onclick = () => el("import").close();
el("import-form").onsubmit = submitImport;

// Ask before drawing anything, so a signed out visitor gets the login form
// rather than a flash of an empty seat list.
api("GET", "/api/session")
  .then((session) => (session.authenticated ? showApp() : showLogin()))
  .catch(() => showLogin());

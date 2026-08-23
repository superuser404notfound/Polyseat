// Polyseat interface.
//
// The daemon pushes a token whenever anything changes and this reloads the
// state. No polling and no framework: the whole page is a handful of cards and
// rebuilding one is cheaper than tracking what moved inside it.
//
// A card is only rebuilt when that seat's state actually differs, though. The
// daemon pushes on every log line, and rebuilding everything each time is what
// made an open panel flicker: it was destroyed and recreated, went back to
// saying "loading", asked the container all over again, and lost anything half
// typed into a field.

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

// One fetch at a time, and always one more after the last thing that asked.
//
// The daemon pushes a token on every change, and during provisioning or an
// install that is many a second. Firing a fetch at each of them let them
// overtake one another, and whichever answered last won: an older snapshot
// arriving after a newer one put the page back to how things had been a
// moment ago. That is what made a seat appear to finish provisioning and
// start again, and what left "installing" on the screen after an install had
// finished.
let refreshing = false;
let refreshPending = false;

async function refresh() {
  if (refreshing) {
    refreshPending = true;

    return;
  }

  refreshing = true;

  try {
    do {
      refreshPending = false;

      // Fetched together, because a seat card and the library view disagreeing
      // about which seats exist looks like a bug in whichever one you read
      // second.
      const [next, pool] = await Promise.all([
        api("GET", "/api/state"),
        api("GET", "/api/library"),
      ]);

      state = next;
      library = pool;
      render();
    } while (refreshPending);
  } catch (err) {
    if (err.unauthorized) {
      showLogin();

      return;
    }

    // Swallowing this is how the empty column happened: the fetch succeeded,
    // rendering threw, and the catch turned a broken page into a silent one.
    console.error(err);
    showError(err.message || String(err));
  } finally {
    refreshing = false;
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

// updateBanner says that a newer Polyseat has been published, and offers to
// install it where that is possible.
//
// It used to say it and stop there, because installing meant rebuilding the
// daemon from a checkout. A package install is a file and a pacman transaction,
// which is something the daemon can do for itself.
//
// The install and the restart are two buttons and not one, and that is the
// whole design rather than an omission. Replacing the binary leaves the running
// process exactly as it was, so the first button is safe at any moment, even
// with two people playing. The second one ends every seat's input broker for a
// moment, so it is offered separately and refused while anybody is streaming.
// This is the one thing the interface does better than host/update.sh: that
// script has to work out whether somebody is playing, and this page already
// knows and already says so on every card.
//
// The link goes to the release rather than repeating its notes here. What
// changed is worth reading before updating, and worth reading in full.
function updateBanner() {
  const release = state.update;
  if (!release) return [];

  const updater = state.updater || {};

  const div = document.createElement("div");
  div.className = "notice";

  const text = document.createElement("span");
  text.textContent =
    `Polyseat ${release.version} was published${describeAge(release.published)}. ` +
    `This machine runs ${state.host.version || "an unknown version"}. `;

  const link = document.createElement("a");
  link.href = release.url;
  link.target = "_blank";
  link.rel = "noreferrer noopener";
  link.textContent = "What changed";

  // While it runs, the lines the daemon reported, newest last. Shown rather
  // than a spinner because the slow part is a download and people reasonably
  // want to know it is moving.
  if (updater.running) {
    const log = document.createElement("div");
    log.className = "muted";
    log.textContent = (updater.log || []).join(" · ") || "starting";
    div.replaceChildren(text, link, log);

    return [div];
  }

  if (updater.error) {
    div.className = "warning";
    const failed = document.createElement("div");
    failed.textContent = "The update failed: " + updater.error;
    div.replaceChildren(text, link, failed);

    return [div];
  }

  // Installed and waiting for a restart. The version on disk is the new one and
  // the version serving is still the old one, which is exactly what the two
  // buttons are for, so this says so rather than looking like nothing happened.
  if ((updater.log || []).length) {
    const done = document.createElement("span");
    done.textContent =
      ` ${release.version} is installed and takes effect when the daemon restarts. `;

    const streaming = updater.streaming || [];
    const button = document.createElement("button");

    if (streaming.length) {
      button.disabled = true;
      button.textContent = "Restart when nobody is playing";
      button.title =
        streaming.length === 1
          ? `${streaming[0]} is streaming right now`
          : `${streaming.join(", ")} are streaming right now`;
    } else {
      button.textContent = "Restart now";
      button.onclick = () => {
        button.disabled = true;
        run(async () => {
          await api("POST", "/api/restart");
          await refresh();
        });
      };
    }

    div.replaceChildren(text, link, done, button);

    return [div];
  }

  // The three reasons there is no button, each with its own sentence. One is a
  // setting somebody chose, one is how this was installed, and one is a release
  // that has no package: telling them apart is the difference between knowing
  // what to do and wondering what is broken.
  let why = "";

  if (!updater.enabled) {
    why = 'Updating from here is off ("web_update" in polyseatd.json). Use host/update.sh. ';
  } else if (!updater.managed) {
    why =
      "This Polyseat was installed from a checkout rather than from the package, " +
      "so pacman has nothing to replace. Use host/update.sh. ";
  } else if (!release.package) {
    why = "That release has no package attached to it, so it cannot be installed from here. ";
  }

  if (why) {
    const note = document.createElement("span");
    note.textContent = why;
    div.replaceChildren(text, note, link);

    return [div];
  }

  const button = document.createElement("button");
  button.textContent = "Install it";
  button.onclick = () => {
    // Asked here rather than posted and refused, so that the one setting that
    // exists to make somebody stop and think actually makes them stop and
    // think. prompt() and not a field on the page: it cannot be filled in by
    // something that is already on the page, and there is nowhere for a browser
    // to have remembered it.
    let password;

    if (updater.needs_password) {
      password = window.prompt(
        `Installing ${release.version} runs pacman as root on this machine. ` +
          "Type the interface password to confirm.",
      );

      // Cancelled, which is a decision and not a failure. No error, no request.
      if (password === null) return;
    }

    button.disabled = true;
    run(async () => {
      await api("POST", "/api/update", password === undefined ? undefined : { password });
      await refresh();
    });
  };

  div.replaceChildren(text, link, button);

  return [div];
}

// describeAge turns a publication date into " 3 days ago", or into nothing at
// all when the date is missing or unreadable.
//
// Separate and defensive because it is the only part of the banner that can
// fail: the version and the link are strings the daemon read out of an answer
// it had already checked, while this is arithmetic on a date that came from
// somebody else's server.
function describeAge(published) {
  if (!published) return "";

  const when = new Date(published);
  if (Number.isNaN(when.getTime())) return "";

  const days = Math.floor((Date.now() - when.getTime()) / 86400000);

  if (days < 0) return "";
  if (days < 1) return " today";
  if (days === 1) return " yesterday";

  return ` ${days} days ago`;
}

// staleBanner offers to bring every out of date seat up to date, as one action.
//
// The seats being behind is one situation, so dealing with it is one button. It
// used to mean opening each card in turn and remembering which had been done,
// which after an update to the daemon is every one of them.
function staleBanner() {
  const stale = (state.seats || []).filter((seat) => seat.stale).map((seat) => seat.name);

  if (!stale.length && !state.provisioning_all) return [];

  const div = document.createElement("div");
  div.className = "warning";

  if (state.provisioning_all) {
    div.textContent =
      "Provisioning the out of date seats, one after another. " +
      "This takes a few minutes each and carries on if you close this page.";

    return [div];
  }

  const text = document.createElement("span");
  text.textContent =
    stale.length === 1
      ? `Seat ${stale[0]} was built by an older version of the daemon. `
      : `${stale.length} seats were built by an older version of the daemon (${stale.join(", ")}). `;

  const button = document.createElement("button");
  button.textContent = stale.length === 1 ? "Provision it" : "Provision them";
  button.onclick = () => {
    button.disabled = true;
    run(async () => {
      await api("POST", "/api/provision-stale");
      await refresh();
    });
  };

  div.replaceChildren(text, button);

  return [div];
}

// describeSession says who is streaming from a seat and what they asked for.
//
// The first question anybody asks about a machine several people share, and until
// now the interface answered it only by implication: the resolution row showed
// two values when somebody was connected and one when nobody was.
//
// An address rather than a name, because Sunshine does not offer one. Moonlight
// gives its name while pairing and Sunshine keeps that against a certificate,
// not against an address, and there is no endpoint for the session in progress.
// The paired devices are listed a few rows below, which is as close as this gets.
function describeSession(session) {
  const parts = [session.app || "something"];

  if (session.width && session.height) {
    parts.push(
      session.fps
        ? `${session.width}x${session.height} at ${session.fps} Hz`
        : `${session.width}x${session.height}`,
    );
  }

  if (session.hdr === "1") parts.push("HDR");
  if (session.peer) parts.push(`from ${session.peer}`);

  const started = session.started && new Date(session.started);
  if (started && !Number.isNaN(started.getTime())) {
    const minutes = Math.floor((Date.now() - started.getTime()) / 60000);
    const since = started.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

    parts.push(minutes < 1 ? `since ${since}` : `since ${since}, ${minutes} min`);
  }

  return parts.join(", ");
}

// -------------------------------------------------------------------- render

function render() {
  if (!state) return;

  el("hostname").textContent = state.host.hostname;

  const observer = el("observer");
  observer.hidden = false;
  observer.textContent = "uhid observer: " + state.observer;
  observer.className = "pill " + state.observer;

  // Always shown, including when there is nothing to show. A machine where no
  // card was found builds seats that come up and encode in software, and the
  // absence of a pill is not something anybody notices.
  const gpu = el("gpu");
  gpu.hidden = false;
  gpu.textContent = state.host.gpu || "no GPU detected";
  gpu.className = "pill " + (state.host.gpu_vendor ? "online" : "offline");

  // What is running, not what is installed. An install builds a new binary and
  // leaves the old process serving, so this is the line that settles it.
  el("version").textContent = "polyseatd " + (state.host.version || "unknown");

  // The list is absent rather than empty when there is nothing to warn about,
  // and calling map on that is what silently broke the whole page once.
  el("warnings").replaceChildren(
    ...updateBanner(),
    ...staleBanner(),
    ...(state.warnings || []).map((text) => {
      const div = document.createElement("div");
      div.className = "warning";
      div.textContent = text;
      return div;
    }),
  );

  const seats = el("seats");

  // Before the empty case below returns, or deleting the last seat would leave
  // its card and everything it had loaded behind.
  forgetMissingSeats();

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

  const nodes = state.seats.map(cardFor);

  // Only touch the DOM when the set of cards actually differs.
  //
  // The daemon pushes a token on every log line, so this runs often, and it
  // used to rebuild every card each time. That is what made the interface
  // flicker: an open panel was destroyed and recreated, went back to saying
  // "loading" and asked the container all over again, and anything half typed
  // into a field was gone. Reusing the node for a seat whose state has not
  // changed leaves all of that alone.
  const unchanged =
    nodes.length === seats.children.length &&
    nodes.every((node, i) => seats.children[i] === node);

  if (!unchanged) seats.replaceChildren(...nodes);

  // Whether or not any card was rebuilt.
  refreshOpenLogs();

  renderLibrary();
}

// Cards, keyed by seat name, kept as long as the seat looks the same.
let cards = new Map();

function cardFor(seat) {
  const json = JSON.stringify(seat);
  const kept = cards.get(seat.name);

  if (kept && kept.json === json) return kept.node;

  const node = card(seat);
  cards.set(seat.name, { json, node });

  return node;
}

// A deleted seat should not leave its card, or what it was showing, behind.
function forgetMissingSeats() {
  const live = new Set(state.seats.map((seat) => seat.name));

  for (const name of [...cards.keys()]) {
    if (!live.has(name)) {
      cards.delete(name);
      softwareSeen.delete(name);
      openSoftware.delete(name);
      openPairing.delete(name);
      openLogs.delete(name);
      logViews.delete(name);
    }
  }
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
            "The games already on this host come in by themselves once their " +
            "Steam library is watched, which the daemon does for you where it " +
            "can.",
        }),
        // Worth the most here of anywhere: an empty pool on a machine that has
        // a Steam library full of games is exactly the state this is for.
        ...watching(library),
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

  body.replaceChildren(...[table, note, ...watching(library), aside].filter(Boolean));
}

// The libraries outside the seats: which ones the pool watches, and which ones
// are sitting on this host without being watched.
//
// Both halves are here because the daemon adopts one on its own when it can, so
// what the interface has to show is the outcome of a decision somebody else
// made: what was taken, how to take it back, and, when nothing was taken, that
// there was something to take.
function watching(pool) {
  const sources = pool.sources || [];
  const candidates = pool.candidates || [];

  // Which one receives has to be said out loud: only one can, because the same
  // game cloned into two folders of one Steam client is installed twice as far
  // as that client is concerned.
  const receiving = pool.receiving || "";

  const out = [];

  if (sources.length) {
    out.push(
      Object.assign(document.createElement("p"), {
        className: "hint",
        textContent:
          "Also watching, so a game updated there reaches the seats by itself:" +
          (receiving
            ? " games from the seats are cloned back into " +
              receiving +
              ", where the host's Steam finds them, and it picks up a title it" +
              " did not install itself the next time it starts." +
              (sources.length > 1
                ? " The other watched libraries are read only."
                : "")
            : ""),
      }),
    );

    const list = document.createElement("ul");
    list.className = "devices";

    sources.forEach((path) => {
      const row = document.createElement("li");

      row.append(
        Object.assign(document.createElement("span"), { textContent: path }),
      );

      if (path === receiving) {
        row.append(
          Object.assign(document.createElement("span"), {
            className: "badge",
            textContent: "receiving",
            title:
              "Games installed in a seat are cloned into this one, so the " +
              "sharing goes both ways.",
          }),
        );
      }

      const stop = document.createElement("button");
      stop.className = "quiet tiny";
      stop.textContent = "Stop watching";
      stop.title =
        "Stops taking games from this library and stops cloning games into " +
        "it. Nothing already installed is removed anywhere.";
      stop.onclick = () => {
        if (
          !confirm(
            `Stop watching ${path}?\n\n` +
              `The games it has already given stay in the pool and in the ` +
              `seats. Polyseat will not start watching it again on its own.`,
          )
        ) {
          return;
        }

        run(() => api("POST", "/api/library/unwatch", { path }));
      };

      row.append(stop);
      list.append(row);
    });

    out.push(list);

    return out;
  }

  if (candidates.length === 0) return out;

  // Nothing watched while candidates exist means the daemon looked and held
  // back: two libraries to choose between, one on a filesystem that cannot share
  // blocks with the pool, or one somebody stopped watching by hand. Whichever it
  // was, the way in is the same button, and it is worth pointing at rather than
  // leaving somebody to find.
  out.push(
    Object.assign(document.createElement("p"), {
      className: "hint",
      textContent:
        (candidates.length === 1
          ? "There is a Steam library on this host that the pool is not watching: "
          : "There are Steam libraries on this host that the pool is not watching: ") +
        candidates.join(", ") +
        ". Games in it stay out of the seats until it is watched, and games " +
        "installed in a seat will not turn up in it. Use \u201cWatch a " +
        "library\u201d to bring it in. The daemon does that by itself only " +
        "when there is exactly one and it can share blocks with the pool.",
    }),
  );

  return out;
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
  // While an operation owns the seat, the pill says so and looks like it.
  //
  // It used to take its colour from the state and its words from the operation,
  // and towards the end of provisioning those disagree several times in a few
  // seconds: the session comes up, so the state is briefly running, while the
  // run itself is not finished. The pill went green and back with the word
  // "provisioning" still on it, which reads as finishing and starting over.
  status.className = "pill " + (seat.busy ? "building" : seat.state);
  status.textContent = seat.busy || seat.state;

  head.append(title, name, spacer, status);

  const bar = progressBar(seat);

  node.append(
    head,
    ...(bar ? [bar] : []),
    facts(seat),
    actions(seat),
    // Pairing first: a seat is set up once and then played on, and until a
    // device is paired with it there is nothing to install software for.
    pairingPanel(seat),
    softwarePanel(seat),
    logPanel(seat),
  );

  return node;
}

// How far a long operation has got, when it can say.
//
// Only installing software reports this. Provisioning is a recipe whose steps
// are named in the log as they run, so a bar would add nothing; an install is
// a download from somebody else's server, where a spinner and a line of text
// leave you unable to tell slow from stuck.
function progressBar(seat) {
  if (!seat.busy || typeof seat.progress !== "number" || seat.progress < 0) {
    return null;
  }

  const wrap = document.createElement("div");
  wrap.className = "progress";

  const fill = document.createElement("span");
  fill.style.width = Math.min(100, Math.max(0, seat.progress)) + "%";

  wrap.append(fill);
  wrap.title = seat.busy + ": " + seat.progress + "%";

  return wrap;
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
      // Opening it is somebody asking, so ask the seat.
      loadSoftware(seat, body, true);
    } else {
      openSoftware.delete(seat.name);
    }
  };

  // Rebuilding a card is not somebody asking. Draw what was last read instead,
  // so a card that changed for an unrelated reason does not send the panel back
  // to "loading" and does not exec into the container again.
  if (details.open) loadSoftware(seat, body, false);

  return details;
}

// What was last read from each seat, so a redraw costs nothing.
let softwareSeen = new Map();

async function loadSoftware(seat, body, fresh) {
  const seen = softwareSeen.get(seat.name);

  if (seen) {
    drawSoftware(seat, body, seen);

    if (!fresh) return;
  } else {
    body.replaceChildren(note("loading"));
  }

  let status;

  try {
    status = await api("GET", `/api/seats/${seat.name}/software`);
  } catch (err) {
    if (err.unauthorized) return showLogin();

    body.replaceChildren(note(err.message));

    return;
  }

  softwareSeen.set(seat.name, status);
  drawSoftware(seat, body, status);
}

// Draw a status that has already been read. Split out from reading it so that
// redrawing a card costs nothing and shows no gap.
function drawSoftware(seat, body, status) {
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

  // Two sources, drawn one after the other and independent of each other. A
  // seat with no flatpak can still be given an AppImage, so a problem with one
  // of them is said in its own section rather than emptying the panel.
  if (!status.flatpak) {
    body.append(
      note(status.flatpakProblem || "Flathub cannot be used in this seat."),
    );
  } else if (installed.length === 0) {
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
          await loadSoftware(seat, body, true);
        });

      item.append(name, remove);
      list.append(item);
    });

    body.append(list);
  }

  const offer = status.flatpak
    ? (status.catalog || []).filter((entry) => !have.has(entry.id))
    : [];

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
          // An install is hundreds of megabytes, and a button that looks
          // untouched for a minute reads as a button that did not work.
          add.disabled = true;
          add.textContent = "Installing";

          try {
            await api("POST", `/api/seats/${seat.name}/software`, { id: entry.id });
            await loadSoftware(seat, body, true);
          } finally {
            add.disabled = false;
            add.textContent = "Install";
          }
        });

      item.append(name, add);
      grid.append(item);
    });

    body.append(grid);
  }

  const results = document.createElement("div");

  if (status.flatpak) {
    body.append(searchForm(seat, body, results, have), results);
  }

  drawAppImages(seat, body, status);
}

// The second way software arrives: one file, from wherever its author publishes
// it.
//
// Here because a good many emulators are published that way and as nothing
// else, so a seat that can only install flatpaks is a seat where the answer to
// "can I play this" is no for reasons that have nothing to do with the game.
// There is no catalog to browse and no search to run: an AppImage has no index
// anywhere, so what this can offer is the address somebody found and the list of
// what that produced.
function drawAppImages(seat, body, status) {
  const images = status.appImages || [];

  body.append(
    note(
      "AppImages, kept in the player's Applications folder. One is also " +
        "picked up when it is downloaded inside the seat, so a file saved " +
        "with Firefox turns up here and in Moonlight within a minute.",
    ),
  );

  if (status.appImageProblem) {
    body.append(note(status.appImageProblem));
  }

  if (images.length > 0) {
    const list = document.createElement("ul");
    list.className = "devices";

    images.forEach((image) => {
      const item = document.createElement("li");

      const name = document.createElement("span");
      name.textContent = image.size
        ? `${image.name} (${image.size})`
        : image.name;
      name.title = image.path;

      const remove = document.createElement("button");
      remove.textContent = "Remove";
      remove.className = "danger";
      remove.onclick = () =>
        run(async () => {
          await api(
            "DELETE",
            `/api/seats/${seat.name}/appimages/${encodeURIComponent(image.file)}`,
          );
          await loadSoftware(seat, body, true);
        });

      item.append(name, remove);
      list.append(item);
    });

    body.append(list);
  }

  const form = document.createElement("form");
  form.className = "pair";

  const address = document.createElement("input");
  address.placeholder = "https://.../Something-x86_64.AppImage";
  address.autocomplete = "off";
  address.spellcheck = false;
  address.required = true;
  address.type = "url";

  const submit = document.createElement("button");
  submit.className = "primary";
  submit.textContent = "Download";

  form.append(address, submit);

  form.onsubmit = (event) => {
    event.preventDefault();

    run(async () => {
      // A download of several hundred megabytes, so the button says what it is
      // doing rather than sitting there looking untouched. How far it has got
      // is the bar on the seat card.
      submit.disabled = true;
      submit.textContent = "Downloading";

      try {
        await api("POST", `/api/seats/${seat.name}/appimages`, {
          url: address.value.trim(),
        });

        address.value = "";
        await loadSoftware(seat, body, true);
      } finally {
        submit.disabled = false;
        submit.textContent = "Download";
      }
    });
  };

  body.append(form);
}

// Search Flathub by words rather than by application id.
//
// The field here used to want an exact id, which is knowledge nobody has:
// somebody looking for Minecraft does not know it is called
// com.mojang.Minecraft, and there was nothing in this interface that would
// tell them. Pasting an id still works, because searching for one finds it.
function searchForm(seat, body, results, have) {
  const form = document.createElement("form");
  form.className = "pair";

  const query = document.createElement("input");
  query.placeholder = "Search Flathub, for example minecraft or emulator";
  query.autocomplete = "off";
  query.spellcheck = false;
  query.required = true;

  const submit = document.createElement("button");
  submit.className = "primary";
  submit.textContent = "Search";

  form.append(query, submit);

  form.onsubmit = (event) => {
    event.preventDefault();
    submit.disabled = true;
    results.replaceChildren(note("searching Flathub"));

    run(async () => {
      try {
        const found = await api(
          "GET",
          `/api/seats/${seat.name}/software/search?q=${encodeURIComponent(
            query.value.trim(),
          )}`,
        );

        drawResults(seat, body, results, found.results || [], have);
      } finally {
        submit.disabled = false;
      }
    });
  };

  return form;
}

function drawResults(seat, body, results, found, have) {
  if (found.length === 0) {
    results.replaceChildren(note("Nothing on Flathub matches that."));

    return;
  }

  const list = document.createElement("ul");
  list.className = "devices";

  found.forEach((entry) => {
    const item = document.createElement("li");

    const name = document.createElement("span");
    name.textContent = entry.summary
      ? `${entry.name} - ${entry.summary}`
      : entry.name;
    name.title = entry.id;

    item.append(name);

    // Saying so is more use than a button that would fail or reinstall.
    if (have.has(entry.id)) {
      const already = document.createElement("span");
      already.className = "hint";
      already.textContent = "installed";
      item.append(already);
    } else {
      const add = document.createElement("button");
      add.textContent = "Install";
      add.onclick = () =>
        run(async () => {
          // An install is hundreds of megabytes, and a button that looks
          // untouched for a minute reads as a button that did not work.
          add.disabled = true;
          add.textContent = "Installing";

          try {
            await api("POST", `/api/seats/${seat.name}/software`, { id: entry.id });
            await loadSoftware(seat, body, true);
          } finally {
            add.disabled = false;
            add.textContent = "Install";
          }
        });

      item.append(add);
    }

    list.append(item);
  });

  results.replaceChildren(list);
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
  //
  // The codecs beside it because naming only one reads as a limit. Sunshine
  // probes for three and offers whichever the client asks for, so a seat that
  // said "h264_nvenc" looked as though H.265 were out of the question when it
  // was there all along.
  if (seat.encoder) {
    const software = seat.encoder.startsWith("lib");
    const codecs = (seat.codecs || []).join(", ");

    row(
      "Encoder",
      software
        ? seat.encoder + " (software, the GPU path is broken)"
        : codecs
          ? `${seat.encoder} (${codecs})`
          : seat.encoder,
      software ? "flag bad" : null,
    );
  }

  row("Input broker", seat.broker, seat.broker === "running" ? null : "flag");
  // What each device calls itself, not the kernel's number for it. eventN says
  // nothing about whether a controller arrived, which is the only question this
  // row is ever asked. The number is kept as the hover text, because it is what
  // every log line and every Incus attachment is named after.
  const devices = seat.devices || [];

  if (devices.length === 0) {
    row("Devices", "none attached");
  } else {
    const names = document.createElement("span");

    devices.forEach((device, i) => {
      const span = document.createElement("span");
      span.textContent = (i ? ", " : "") + (device.name || device.node);
      span.title = device.node;
      names.append(span);
    });

    row("Devices", names);
  }

  // What the session comes up with, and what it is running at now. They differ
  // whenever somebody is connected, because the output is virtual and becomes
  // whatever the client asked for.
  row(
    "Resolution",
    seat.output && seat.output !== seat.resolution
      ? `${seat.output} now, ${seat.resolution} when idle`
      : seat.resolution,
  );
  if (seat.session) row("Streaming", describeSession(seat.session));

  row("Shared library", seat.library ? "yes" : "no");

  // Only worth a row where it is a choice. On a plain uplink every seat is
  // isolated and saying so on every card would be repeating one fact about the
  // machine once per seat.
  if (state.host.uplink_bridged) {
    row(
      "Reaches the host",
      seat.isolated ? "no, isolated on a macvlan" : "yes, on " + state.host.uplink,
    );
  }

  if (!seat.built) {
    row("Not built yet", "Start builds it, which takes a few minutes");
  } else if (seat.stale) {
    row("Provisioning", "out of date, rebuild this seat", "flag");
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

  // A seat nobody has built yet gets Start and nothing else. Start builds it,
  // and offering Build beside it was offering the same thing twice under two
  // names, one of which read as though something had gone wrong with a seat
  // created a minute ago.
  if (seat.built) {
    button("Rebuild", () => api("POST", `/api/seats/${seat.name}/provision`));
  }

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
      logViews.set(seat.name, pre);
      loadLog(seat.name, pre);
    } else {
      openLogs.delete(seat.name);
      logViews.delete(seat.name);
    }
  };

  if (details.open) {
    logViews.set(seat.name, pre);
    loadLog(seat.name, pre);
  }

  return details;
}

// The log panels that are open, so they can be refreshed on their own.
//
// A log is the one thing on a card that changes without the seat changing, and
// it is the thing somebody watches while provisioning runs: for those few
// minutes the seat's own state does not move at all, it is busy from start to
// finish. Reusing an unchanged card, which is what stopped the interface
// flickering, therefore froze the log exactly when it mattered most and left
// no sign of when the work had finished.
let logViews = new Map();

function refreshOpenLogs() {
  for (const [name, pre] of logViews) {
    loadLog(name, pre);
  }
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

// showLogin puts up the sign in form, or the one that chooses the first
// password when there is none yet. Same section, because they are the same
// moment for whoever is looking at it: this machine wants to know who you are.
function showLogin(setup) {
  if (stream) {
    stream.close();
    stream = null;
  }

  el("app").hidden = true;
  el("account").hidden = true;
  el("login").hidden = false;
  el("hostname").textContent = "";
  el("observer").hidden = true;
  el("gpu").hidden = true;
  el("link").textContent = setup ? "not set up yet" : "signed out";
  el("link").className = "pill offline";

  el("setup-form").hidden = !setup;
  el("login-form").hidden = !!setup;

  if (setup) {
    el("setup-form").password.focus();
  } else {
    el("login-form").username.focus();
  }
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

async function submitSetup(event) {
  event.preventDefault();

  const form = el("setup-form");
  const button = el("setup-submit");

  el("setup-error").textContent = "";

  if (form.password.value !== form.confirm.value) {
    el("setup-error").textContent = "The two passwords are not the same.";
    form.confirm.value = "";
    form.confirm.focus();

    return;
  }

  // Hashing a password is deliberately slow, so say something rather than
  // letting the page look frozen for a second.
  button.disabled = true;

  try {
    await api("POST", "/api/setup", {
      username: form.username.value.trim(),
      password: form.password.value,
      confirm: form.confirm.value,
    });

    form.password.value = "";
    form.confirm.value = "";
    showApp();
  } catch (err) {
    el("setup-error").textContent = err.message;
  } finally {
    button.disabled = false;
  }
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
    if (form.new.value !== form.confirm.value) {
      el("password-error").textContent = "The two passwords are not the same.";
      form.confirm.value = "";
      form.confirm.focus();

      return;
    }

    await api("POST", "/api/password", {
      username: form.username.value.trim(),
      current: form.current.value,
      new: form.new.value,
      confirm: form.confirm.value,
    });

    el("password").close();
    form.current.value = "";
    form.new.value = "";
    form.confirm.value = "";
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
  form.confirm.value = "";
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

  // Stored the other way round: a seat records that it is isolated, the page
  // asks whether it may talk to this machine. The default for a new seat is
  // yes, which is the only reason anybody bridges the uplink.
  form.host_access.checked = seat ? !seat.isolated : true;
  describeHostAccess();

  form.pointer_speed.value = (seat && seat.pointer_speed) || DEFAULT_POINTER_SPEED;
  describePointerSpeed();

  el("editor").showModal();
}

// Says what the network checkbox actually does here, which depends on something
// the seat cannot decide: whether the uplink is a bridge.
//
// On a plain interface every seat gets a macvlan and a macvlan cannot talk to
// the interface it hangs off, so the box is true but powerless and saying so is
// the only honest thing the page can do. Disabling it would suggest the setting
// does not exist.
function describeHostAccess() {
  const host = (state && state.host) || {};
  const note = el("editor-host-access-note");
  const box = el("editor-form").host_access;

  if (!host.uplink_bridged) {
    box.disabled = true;
    note.textContent =
      "Not available: " +
      (host.uplink || "the uplink") +
      " is not a bridge, so every seat takes a macvlan from it and a macvlan " +
      "cannot reach the interface it hangs off. Run host/lan-bridge.sh on the " +
      "host to change that. Seats still reach each other and the rest of the " +
      "network either way.";
    return;
  }

  box.disabled = false;
  note.textContent = box.checked
    ? "This seat is a port on " +
      host.uplink +
      ", so it and the host are on one network segment: they see each " +
      "other's broadcasts and can play the same local multiplayer game. It " +
      "also means the seat can reach whatever the host is listening on."
    : "This seat gets a macvlan on " +
      host.uplink +
      " instead. It keeps its own address, still reaches the rest of the " +
      "network and the other seats, and cannot reach the host or be " +
      "reached by it. That was how every seat worked before.";
}

// The default the daemon uses for a seat that has never been given a number of
// its own. Repeated here rather than fetched, because the slider has to show
// something before anything has been saved.
const DEFAULT_POINTER_SPEED = 0.45;

// Says what the number means. A slider from 0.15 to 1.2 tells somebody nothing
// on its own, and the honest translation is a time: how long the pointer takes
// to cross the screen with the stick held over.
function describePointerSpeed() {
  const speed = Number(el("editor-form").pointer_speed.value);
  const seconds = 1 / speed;

  el("pointer-speed-note").textContent =
    `Crosses the screen in ${seconds.toFixed(1)} seconds at full deflection.` +
    (Math.abs(speed - DEFAULT_POINTER_SPEED) < 0.001 ? " This is the default." : "");
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
    host_access: form.host_access.checked,
    pointer_speed: Number(form.pointer_speed.value),
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
el("editor-form").pointer_speed.oninput = describePointerSpeed;
el("editor-form").host_access.onchange = describeHostAccess;
el("login-form").onsubmit = submitLogin;
el("setup-form").onsubmit = submitSetup;
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
  .then((session) => (session.authenticated ? showApp() : showLogin(session.setup)))
  .catch(() => showLogin(false));

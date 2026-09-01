# Changelog

A version is a git tag, and the daemon is stamped with the tag it was built
from. `polyseatd -version`, the line at the bottom of the web interface and the
first line in the journal all answer with the same string, so there is never a
question of which build is running.

Versions are `MAJOR.MINOR.PATCH`. Before 1.0 the minor number carries anything
that changes behaviour, including changes that need seats to be built again.
When that happens it is written here, because it is the one kind of update that
costs a few minutes per seat rather than a restart.

## 0.14.0

**Getting to the interface began with remembering a port number.** Polyseat is a
web page, and the machine it runs on had nothing on its own desktop that pointed
at it: no entry in the menu, no icon, nothing to click. Whoever sat down at the
host typed an address they had to know already, or went looking for the line the
installer printed on the day it was installed.

**There is now a Polyseat entry in the application menu**, placed by
`host/install.sh` into `/usr/local/share` and by all three packages into
`/usr/share`. It opens `https://localhost:47800`, which is the host talking to
itself.

**No seat has to be built again.** This is two files and where they are put.

- **localhost, and not the address the installer prints.** That one is this
  machine's address on the network, meant for the phone or the laptop the seats
  are managed from. This file is only ever read on the host itself, and the
  default listens on every interface, so the loopback is one of them.

- **The port is written out a third time**, because a `.desktop` file cannot
  read a config. `install.sh` and `polyseat.install` already carry a copy for
  the same reason. A host that has moved `listen` elsewhere edits the `Exec`
  line.

- **`Exec=xdg-open` rather than `Type=Link`.** A link entry is how a file
  manager writes down a URL, and it is not shown in an application menu, which
  is the whole point of the file.

- **One main category and not two.** Game and System both fit something that
  manages gaming seats, and a menu given both is entitled to show the entry
  twice; `desktop-file-validate` says so. Game is the one somebody looking for
  this would open, which is what decides it.

- **The icon was drawn at 22 pixels and checked at 128.** A screen that is
  switched on, divided into four seats with one of them taken, in the blue and
  the amber `internal/web/style.css` already uses, so the icon and the page it
  opens agree. A bezel outline and a monitor stand were both in an earlier draft
  and both closed up into a smear at panel size.

- **The icon is placed before the entry, and that order is a fix rather than
  tidiness.** A running desktop reads a new entry the moment it appears, and one
  that names an icon which is not on disk yet is remembered without a picture.
  On Plasma it stays that way until the shell is restarted, long after the file
  has arrived. Found the way these things are found: on the desktop this was
  written on, with the two files half a second apart.

- **There is no tray icon**, which is where this started. It would want a second
  binary with a GUI toolkit and C libraries, in a project whose package has
  three dependencies and is meant to run on a host with no desktop at all; a
  tray that Plasma has, GNOME needs an extension for and sway does not have; and
  a polkit rule so that a user session may restart a system service, which is a
  security decision rather than a convenience. The interface already has a
  restart button, and it is reachable from the entry this release adds.

- **CI reads both files with the tools a desktop uses.**
  `desktop-file-validate` on the entry, and librsvg rendering the icon at 22 and
  128 pixels. Neither kind of mistake fails a build, a test or `go vet`: they
  fail as an entry that is not in the menu, or is in it without a picture, on a
  machine nobody here is sitting at.

**What is proven and what is not.** Both files were installed on the Plasma 6
Wayland desktop this was written on, and the entry is in the menu with its icon.
`host/test-install.sh` and `host/test-package.sh` gained three assertions
between them and neither has been run against a virtual machine since. The
`.deb` and the `.rpm` are in the position they have been in since 0.9.0: built,
reasoned about, and never installed on a Debian or a Fedora machine.

## 0.13.3

**The screen that says Polyseat is restarting ended before the restart did.**
It covered the page, counted the seconds, and after eight of them reloaded
whether or not anything had happened. Often enough the daemon was still on its
way out at that point, so the page reloaded into the old process and that
process then died underneath a page that had just been told everything was
fine: cards that were true a moment ago, an event stream that dropped again,
and no sign left that a restart had been asked for.

Eight seconds was a guess about a stop that has no fixed length. The unit
allows thirty seconds for it and this daemon can use twenty-five of them
waiting for the seat manager to let go, the transient unit that does the
restarting fires a second out and then queues like any other systemd job, and
a machine with something heavy running is slower than all of that.

**The daemon now says which daemon it is, and the page waits for that to
change.** Every process names itself once at startup, gives the name out on
`/api/session` and puts it in the answer that agrees to a restart, so the page
holds the name of the process that agreed to go. Whatever answers afterwards, a
different name is a different process and the restart is over. Nothing is read
off a clock.

**No seat has to be built again.** This is the daemon and the page it serves.

- **The name is eight random bytes and not the startup time.** The startup time
  would have done the same job while telling anybody who reached the port how
  long the machine had been up, and the endpoint that carries the name answers
  before there is a session: the page has to know which of the two interfaces
  to draw before it can ask for anything else.

- **Having seen the daemon go and answer again still counts, but no longer on
  its own.** It is what decides a restart into a build old enough to have no
  name, and there it is the only thing there is. Where there are two names to
  compare, they decide it: a dropped connection is a daemon that is stopping,
  but it is also a laptop's radio, and a process that gives its old name back
  afterwards is the old process either way. Before this, any blip followed by
  any answer was read as a finished restart.

- **Two minutes without an end now says which of the two failures it was.** A
  daemon that answered the whole time is not one that failed to come back, it
  is one that never left, and what did not happen is the transient unit. That
  unit is started with `--collect`, so it is gone by the time anybody looks;
  the page names `journalctl -u polyseat-restart` rather than a status that
  would report nothing.

## 0.13.2

**A seat that had been stopped would not start again, and what said so named
nothing to do with the reason.** The interface showed

    Failed to run: /usr/lib/incus/incusd forklxc joser ... exit status 1

which is a helper and an exit code. The reason was in
`/var/log/incus/<seat>/lxc.log`, a file only root can read: `Unsupported cgroup
layout (hybrid)`. LXC 7 refuses to start any container on a host it calls
hybrid, and one cgroup v1 mount anywhere under `/sys/fs/cgroup` earns that name
even when everything else on the machine is unified.

The mount was nothing the machine's owner had done. `mullvad-daemon` mounts
`net_cls` for its split tunneling every time it starts, has no option to leave
it alone, and puts it back after every reboot and every update. Seats that were
already running when it appeared kept running, so what it looked like was one
broken seat rather than a host that could no longer start any. An afternoon went
into the seat, its devices and the Incus version before the layout was even
suspected, which is the failure this release is really about: not the mount, but
that nothing on the way from the button to the log mentioned cgroups.

**The daemon now clears such a hierarchy out of the way itself, and only when
nothing is in it.** An empty hierarchy is evidence that nobody uses the feature
it belongs to, and clearing it costs its owner nothing they would notice. One
with processes in it belongs to somebody who is relying on it, and that one is
left alone and explained instead, with the command that would clear it and what
clearing it would cost. Split tunneling stops working until `mullvad-daemon` is
next started, and the mount comes back when it is.

**No seat has to be built again.** This is in the daemon alone.

- **The check happens after a start has failed, not before one is attempted.**
  Refusing a start because the layout looks wrong would be a claim that it
  cannot work, and that claim is only as good as one reading of a version of LXC
  that has changed before. Get it wrong and a machine that would have run seats
  is told it cannot, with no way for the person in front of it to disagree.
  Asked only after a real failure, it cannot be wrong in that direction: on a
  host where everything works, none of this ever runs.

- **What it looks at, though, is read before the start rather than after it.** A
  failing start destroys the evidence it would be judged on. LXC makes cgroups
  of its own in every hierarchy on the host and moves the attempt's processes
  through them, so a host asked afterwards reports processes that belong to the
  failure itself, and the failure ends up vouching for its own irreparability.
  On hardware it reported two processes, none a second later, in a cgroup that
  no exclusion list would have been written for in advance.

- **Counting who uses a hierarchy counts the child cgroups and never the root.**
  In cgroup v1 every process on the machine is a member of the root of every
  mounted hierarchy, so a host with nothing in `net_cls` still shows several
  hundred there. Counting those would call an untouched hierarchy busy and turn
  the one case that can be fixed into the one case that is refused.

- **Whether the mount is gone is decided by the mount table, not by what
  `umount` printed.** A failed start can remove the mount by itself: it is a
  shared mount, and an unmount inside the namespace LXC builds propagates back
  to the host. `umount` then says "not mounted" and exits 32, which is the
  wanted state arriving by another route. An earlier version of this read that
  as a failure and skipped the retry while standing in front of the result it
  had asked for.

- **`polyseat-report` gains a `cgroup layout` line.** It sits with the kernel
  and the CPU in the Machine section, because it is exactly the kind of fact a
  report exists to carry and no amount of reading a seat's own state would have
  produced it.

## 0.13.1

**The whole machine stuttered for about a second, every 70 seconds, whenever
seats were running.** Reported from the host's desktop and confirmed inside a
seat's game, so it was not a desktop problem: everything drawing on that machine
stopped at the same moment. Found by measurement rather than by reading code,
and what the measurements ruled out matters as much as what they found. No gap
in the scheduling, none in the input path, no error and no throttling from the
card, and the driver answering a query every 250ms straight through it.

What lined up was the period. Six stutters came 70, 71, 69, 71 and 71 seconds
apart, and a watch on every process start found four things on exactly that
clock in one seat: `lutris`, `Xwayland`, `glxinfo` and `vulkaninfo`. Running the
listing command by hand reproduced the stutter on demand.

**Big Picture still came back windowed after a game**, because the fix in 0.13.0
matched an English window title and Steam translates it. A German session calls
the window "Big-Picture-Modus", so the watcher, the sway rule and the launcher
all matched a window nobody had.

**Seats have to be built again.** The recipe is at generation 37, which is what
puts the new watcher and the corrected rule into a seat. A few minutes per seat.
The stutter is fixed in the daemon alone and needs no rebuild.

- **The game listing is kept until Lutris changes on disk.** Asking Lutris what
  it has means starting Lutris, and it is a GTK application: it brings up
  Xwayland and probes the card with glxinfo and vulkaninfo. Three fresh GPU
  contexts once a minute, on a card shared by the host's compositor and every
  seat's. A stat of its database now answers whether anything changed, without
  starting anything.

- **A seat with no Lutris costs a stat.** It used to cost a failed exec a
  minute, and a Lutris that has never been run answered with nothing at all,
  which read as "could not be asked" and had it asked again a minute later.

- **The listing is only remembered when it was really taken**, so one failed
  read does not freeze Moonlight's list until somebody installs a game.

- **Big Picture is found by what happened, not by what it is called.** The
  watcher follows sway's own events: a window loses fullscreen, another takes it
  in the same moment, and when that one closes the first gets it back. That is
  language independent and application independent, and it leaves $mod+f alone,
  because leaving fullscreen by hand is the same first event with nothing after
  it. The whole recorded session from the seat runs as a test.

- **The title match that remains is a pattern the three files share**, and a
  test holds them to it. The cold start branch also fires only the first time it
  sees a window, so a rename by Steam no longer drags a player back into Big
  Picture after they left it.

- **The input broker reads an extended attribute instead of starting getfacl.**
  Two seats meant 23 getfacl processes a second, forever, to ask whether a
  device node still carries an access list naming somebody. Measured at 885us a
  call against 0.7us for the read. It also fixes what that call had wrong: it
  looked only at named users, and an entry naming a group opens a node exactly
  as far.

## 0.13.0

**A seat's software came from three places and the update knew about two of
them.** pacman does not know a flatpak exists, nothing in the daemon ever ran
`flatpak update`, and no timer inside a seat does either, so a seat could report
itself up to date with every application the player installed months old. Found
by reviewing the update path rather than reported.

**And Big Picture came back windowed after every game.** Reported from a couch,
with a photograph: quit a game and Steam Big Picture is an ordinary tiled window
next to the welcome terminal. Not a Steam problem, and not the title race the
existing helper was written for.

**Seats have to be built again.** The recipe is at generation 36, so every seat
is marked stale and wants provisioning. That is what puts the new helper in it
and adds the line that starts it. A few minutes per seat, and the flatpak half
of this needs no rebuild at all.

- **The check asks Flathub what is waiting, and the update installs it.** As the
  player, because a flatpak installed with `--user` lives in their home and
  root's installation in a seat is empty. The card counts it separately,
  "3 packages, 1 flatpak", because the two are updated by different things.

- **Applications only, not runtimes.** A runtime update is fetched by the update
  either way, and a count of three that means Discord and two things called
  `org.freedesktop.Platform` is not one anybody can act on.

- **A flatpak failure does not fail the update.** By the time it runs, the
  packages and Sunshine are installed. A Flathub outage marking the seat as
  failed would also skip the session restart that makes the rest of it take
  effect, so it is a line in the seat's log and a count that stays where it was,
  which is the seat saying it is still behind, which it is.

- **sway allows one fullscreen container per workspace.** So a game going
  fullscreen is sway taking fullscreen away from Big Picture, and nothing gave
  it back: the rule in the configuration fires when a window is mapped, and that
  window was mapped long before. `polyseat-bigpicture` stops insisting once it
  has seen fullscreen, deliberately, so that leaving Big Picture for the desktop
  does not drag you back into it.

- **`polyseat-bigpicture-watch` watches instead of polling.** It reacts to a
  fullscreen window closing, which is a game ending, and to a window becoming
  Big Picture by being mapped or renamed as it, which is the cold Steam case the
  title rule cannot catch. It ignores `fullscreen_mode` changing on a window
  that stays, because that is `$mod+f` and undoing it would mean killing the
  helper to use the seat.

- **The Sunshine lookup can no longer spend a seat's whole turn.** Both halves
  of a look ran on one context, so a request to GitHub that hangs rather than
  refuses left nothing for the seat, and the card reported "the seat could not be
  asked" about a seat nobody had asked. Twenty seconds for the lookup now,
  inside whatever the caller allowed.

## 0.12.2

**0.12.1 stopped the update check from working at all, and this puts it back.**
The directory it started making for each run is one only its owner may enter,
and pacman has not downloaded as its owner since 6.1. Anybody who installed
0.12.1 sees every seat report a failed sync; this is the fix and there is
nothing else in it.

**Seats are not touched by this.** Nothing here changes how one is built, so an
existing machine updates with a restart and no seat has to be provisioned again.

- **`mktemp -d` makes a directory at 0755 now.** It makes one at 0700, and the
  download runs as `alpm` in a sandbox because Arch ships `DownloadUser` set, so
  the sync stopped before a byte was fetched:

  ```
  error: could not open file .../sync/download-XXXX/core.db.part: Permission denied
  error: failed to setup a download payload for core.db
  ```

  0755 is what pacman's own `/var/lib/pacman/sync` has, for the same reason.
  Measured in a seat with the two modes run side by side, 0700 exit 1 and 0755
  exit 0. The test asks the same question the download user was asking, and
  fails against 0.12.1 with `mode is 700`.

- **The card reports the line that names the file.** A failed sync ends with
  "failed to synchronize all databases (failed to retrieve some files)", which
  is the last line and the least useful one: no database, no mirror, no reason.
  0.12.1 put exactly that on the card. The lines above it carry all three, and
  the first of them is what is shown now.

## 0.12.1

**Both seats said the package mirrors could not be reached, and they were
reachable.** Reported against 0.12.0 from a running machine, with the same sync
answering in two seconds when it was run inside the seat by hand. Pressing Check
for updates took a very long time to arrive at the same sentence. Nothing about
the network was wrong.

**Seats are not touched by this.** Nothing here changes how one is built, so an
existing machine updates with a restart and no seat has to be provisioned again.

- **The check ran in one fixed directory whose first act is `rm -rf`.** Two
  callers start it and neither knows about the other: the six hour pass, and the
  button. The pass does not mark a seat busy and the button only refuses a seat
  that is busy, so both can run in one seat at once, and the second one deletes
  the sync database the first is in the middle of writing. pacman stops on a
  file that vanished under it, and what the seat reports is a failure of the
  network. It is also the one shape of this nobody can reproduce afterwards: a
  command somebody runs themselves collides with nothing. Every run gets a
  directory of its own from `mktemp -d` now, and takes it away again.

- **Whatever pacman said went to `/dev/null`,** and every non-zero exit became
  the same sentence about mirrors. A name that will not resolve, a signature
  that will not check, a full disk and a database deleted underneath all arrived
  as one guess, which read as a measurement and was not one. The last line of
  what pacman said is what the card shows now. The old sentence is kept for the
  case it was always right about: pacman failing with nothing to say.

- **The wait is bounded at 45 seconds.** pacman works through the mirrorlist
  with a timeout per connection and no bound of its own, and the button holds
  the request open for all of it, so a seat whose sync fails slowly looked like
  a button that had hung. A working sync of the four databases is two seconds.

- **The tests run the real script against a stubbed pacman and timeout,** and
  the one that matters took two goes to measure anything. Two runs started at
  the same instant do their setup together and never collide; a marker file of
  one name is replaced by the run that deleted it, so the first finds somebody
  else's and calls it its own. Staggered by 150ms with a marker per run, it
  fails against the old fixed directory with the exit code the seats reported.

## 0.12.0

**Files from the host now have a way into a seat.** Every other route into a
seat comes from somewhere else: Flathub, an address, another seat. What somebody
already had on their own disk had none, and the answers were `incus file push`
at a terminal or a share to set up on both ends. Asked from a machine with an
emulator installed in a seat and its saves and mods sitting on the host.

**Seats do not have to be built again.** The recipe is unchanged at generation
35, and `~/Downloads` has been in every seat since long before this. Restart the
daemon and the panel is there.

- **A Files panel on every seat card.** Drop a file or a whole folder on it, or
  pick one with the two buttons, and it goes into the player's `~/Downloads`
  inside that seat with the folder it came in kept, which is what a save is: a
  directory named after a title id, and a mod is a tree. There is a progress
  line while it goes up, because a folder of games is minutes of somebody's
  uplink and a page that says nothing for that long reads as one that did
  nothing.

- **Downloads and no further, on purpose.** Where a save belongs differs per
  emulator, per install method and per title. A daemon that wrote into those
  would have to know all three and would be wrong about them after the next
  release of any one of them. From `~/Downloads` the player moves it with the
  file manager the seat already has, and an AppImage does not even need that:
  the sweep that already adopts what is downloaded inside a seat adopts this
  too, and it turns up in Moonlight within a minute.

- **Nothing is held on the way through.** The parts of the upload are read one
  at a time and streamed straight into the container, so a drop of any size
  costs the daemon no memory and the host no temporary copy. `ParseMultipartForm`
  would have written every part to the host's disk first, which is the wrong
  disk and twice the space.

- **The name of every file is checked rather than repaired.** A path that is
  absolute, holds `.` or `..` or an empty component, is hidden at the top level,
  carries control characters, is not valid UTF-8, or is longer or deeper than a
  path may be, is refused and named. Go's own `Part.FileName` is deliberately
  not what is used: it applies `filepath.Base`, which turns `../../etc/passwd`
  into a file called `passwd` and writes it. One refused name costs that file
  and not the drop.

- **The restart screen appears when the restart is started from the Host
  dialog.** It always did what it was told and never showed it: a dialog opened
  with `showModal` is in the top layer, where nothing the page paints can reach
  it, so the overlay was drawn underneath and the page simply reloaded half a
  minute later. The same button in the banner at the top has always worked,
  which is what made it look like two buttons. `showFarewell` had already met
  this wall and closed the dialog by name; that is one rule in one place now,
  and the login form uses it too.

## 0.11.0

**A seat has two menus and only one of them was being kept honest.** Reported
against 0.10.0 from a running machine: Discord installed from the web interface,
and then not in the launcher inside the seat. Two unrelated causes that look
identical from a couch, and neither of them says the launcher is the reason.

**Seats have to be built again.** The recipe is at generation 35, so every seat
is marked stale and wants provisioning. A machine that has not yet rebuilt for
0.10.0 pays for both in the same pass.

- **The launcher could not see any flatpak the player installed.** Their desktop
  entries live under `.local/share/flatpak/exports/share`, which is in
  `XDG_DATA_DIRS` in `polyseat-sway.service` and nowhere else. The launcher is
  also opened by a Sunshine prep command, which is what picking Desktop in
  Moonlight does, and that one inherits Sunshine's unit and falls back to the two
  system directories. So the menu listed what the distribution had installed and
  nothing else, while `flatpak run` started the same application from a terminal
  without complaint. `polyseat-launcher` sets the variable itself now, the way it
  already sets `XDG_RUNTIME_DIR` and `WAYLAND_DISPLAY`: its callers do not agree
  about the environment, and one of them arrives with no session environment at
  all. The `env` block in `apps.json` fixed the `PATH` half of this same problem
  and never the other half.

- **What was installed afterwards was missing from a menu already on screen.**
  fuzzel reads the desktop entries once, when it starts, and the session opens
  one and leaves it sitting on the overlay layer. `show` does nothing while one
  is running, so a flatpak installed after that stayed invisible until somebody
  happened to dismiss the launcher and open it again. There is a `refresh` verb
  now, and the daemon calls it after an install or a removal, beside the call
  that rebuilds Moonlight's list. Moonlight's list was already being kept up to
  date; this is the same thing for the menu on the other side of the stream.

- **refresh restarts the launcher only if one is open.** An install finishing
  while somebody is playing must not put a menu over their game, and a menu one
  entry out of date is the smaller of the two failures. `hide` waits for the
  process to actually be gone as well, because `pkill` returns before that and
  `show` does nothing while one still runs, which made the restart a race that
  closed the launcher and put nothing in its place.

- **Three tests, and all three fail without the change.** The environment fuzzel
  really gets when Sunshine opened it, rather than the script containing the
  right word, and both halves of what `refresh` has to do. The stubs for it
  answer from state the script can change, so a `refresh` that did nothing at all
  cannot pass them.

## 0.10.0

**Flatpak in a seat has to be paid for differently now.** Upstream removed the
thing this project was using, on a Wednesday, and the first sign of it here was
that a brand new seat installed no packages at all.

**Seats have to be built again.** The recipe is at generation 34, so every
existing seat is marked stale in the interface and wants provisioning again.
That is a few minutes per seat and it is not optional: a seat built by
generation 33 still has the removed package installed, and its next update would
stop on it.

- **A new seat installed nothing, and said `target not found: bubblewrap-suid`.**
  bubblewrap 0.12.0 removed the setuid build outright on 2026-08-26, and Arch
  dropped the `bubblewrap-suid` package the same day. A seat syncing a package
  database from after that date asks for a package that no longer exists, pacman
  refuses the whole transaction over the one missing target, and so not one of
  the sixty-odd session packages arrives. Nothing about the seat is wrong; the
  package simply stopped existing between one build and the next.

- **Seats carry `security.nesting=true` instead.** This is the fix that
  `docs/security.md` has described since M4 as the obvious one and declined to
  take, in favour of a setuid bwrap. The reasoning behind that preference has
  not changed and no longer has anywhere to go: the setuid build was deprecated
  in 0.11.2 after CVE-2026-41163 and is gone in 0.12.0, so keeping it would mean
  building an abandoned version with an unfixed advisory in every seat. One key
  on the container is the smaller thing to own than that. What it widens, and
  what it does not, is written out in `docs/security.md`.

- **Provisioning removes the setuid package where it finds one.** It is orphaned
  now that nothing carries it, and it conflicts with the plain `bubblewrap` that
  replaces it, which pacman will not resolve on its own. Provisioning takes the
  old one out with `-Rdd` and the same transaction puts the plain one back.

- **Proton is not involved in any of this**, in either direction. The mount that
  fails needs `--unshare-pid`, Steam's pressure-vessel does not use it, and
  flatpak does. That was true when the setuid binary was the answer and it is
  true now.

## 0.9.1

**Installed games went missing from Moonlight.** Reported against 0.9.0 from a
running machine: five of six Steam titles in the list, and the sixth an
ordinary game that had been playable an hour before. It looks exactly like a
limit on how many apps a client will show, which is what it was first taken
for, and it is not one. There is a limit, at sixty, and it says so in the log
when it bites.

**Seats are not touched by this.** Nothing here changes how one is built, so an
existing machine updates with a restart and no seat has to be provisioned
again. The list is rebuilt the next time nobody is streaming, which is the
soonest Sunshine can be told to reread it without ending somebody's session.

- **`StateFlags` is a bitfield and the scan compared the whole of it against
  4.** Fully installed is bit 4, and a title carries more than that bit the
  moment anything else is also true of it: 6 with an update waiting, 68 while
  it is running, 1028 while an update is starting. Every one of those is a game
  whose files are on the disk and which Steam will start, and every one of them
  was dropped without a word. The installed bit now has to be set, and none of
  the three that say the files are not really there: 1 uninstalled, 32 files
  missing, 128 files corrupt. An update waiting is deliberately not among them,
  because picking the game starts Steam, which updates it and then runs it.

- **A game on a second drive was never looked for.** The scan read the two
  directories it knew about, and where Steam keeps its other libraries is
  written in `libraryfolders.vdf` and nowhere else. That file is now read, from
  the two known directories rather than from the ones it names, because Steam
  writes the whole list into each of them. Nothing changes for a seat with one
  library, which is every seat that has only ever used the shared one.

- **Both are tested by running the scan rather than by reading it.** It is
  Python inside a Go string, so checking it for the words it contains is not a
  test, and neither of these faults is one line further out than the comparison
  that caused it.

- **The hardware report says which package manager the host has.** `PRETTY_NAME`
  says "Linux Mint 22"; this says whether anything in `host/` knew what to do
  with it, which is the question a report from a machine that is not the
  author's is being asked. An "unknown" on a machine plainly based on Debian is
  the report saying where to look.

- **The hardware issue template asks what the host runs**, and names Debian and
  Fedora beside AMD as paths nobody has run. Three independent gaps rather than
  one, and a machine that is two of them at once is doubly worth hearing about.

- **`host/distro.sh` no longer carries a version number it never read.**
  Nothing branches on one: whether Incus is in the repositories is answered by
  asking the repositories, which does not go stale the way a table of release
  numbers would. What shellcheck asks for is now said rather than suppressed
  wholesale.

## 0.9.0

**Debian and Fedora hosts.** Asked for on Reddit, where the Arch requirement was
the first thing raised every time this was posted. The host scripts now work out
which of the three package managers this machine has and speak that one, and the
release carries a `.deb` and an `.rpm` beside the Arch package.

**Seats are not touched by this**, and that is the whole reason it was a small
job rather than a rewrite. A seat is an Incus container built from
`archlinux/current` on every host, so every `pacman` call in the daemon runs
inside a container and means the same thing whatever the machine under it runs.
An existing machine updates with a restart and no seat has to be provisioned
again.

**Arch is still the only one that has been run on real hardware.** Debian and
Fedora are covered by `host/test-distro.sh`, which replaces every package
manager with a script that records how it was called; that proves the right
commands are issued with the right arguments and not that the resulting machine
works. Nobody has installed the `.deb` on a Debian box or the `.rpm` on a Fedora
one. This is the same shape of gap `docs/amd.md` describes, and it is written
down here rather than discovered by whoever goes first.

- **`host/distro.sh` is the table, and it is the only place that branches.**
  Everything in `host/` that said `pacman` asks a function instead, so a fourth
  distribution is a row rather than an edit to five scripts. `ID` in
  `/etc/os-release` decides, then `ID_LIKE`, which places CachyOS, EndeavourOS,
  Mint and Rocky without naming any of them.

- **A machine that is none of the three is refused before anything changes.**
  There was no check at all until now: a Debian host got as far as the first
  `pacman` call and died with `pacman: command not found` in the middle of a
  step, which reads like Polyseat is broken rather than like this machine is not
  one it knows. It now says what it found and what is supported, and stops.

- **Refreshing before installing is per distribution, not per taste.** Arch
  still does not, because `-Sy` and then an install without an upgrade is the
  partial upgrade Arch warns about. Debian does, because it has no such hazard
  and it has the opposite one: an install against lists fetched months ago fails
  on a 404 for a version that has since been superseded. `dnf` refreshes itself.

- **Removing leaves dependencies alone on all three, and one of them had to be
  told.** `pacman -R` rather than `-Rs` and `apt-get remove` both do it by
  default. `dnf` ships `clean_requirements_on_remove=True`, so a plain
  `dnf remove polyseat` behaves like `pacman -Rs` and would take Incus, and
  every container on the machine with it, off a host where Polyseat pulled it
  in. That is asked for outright now, and the test that holds it down is the
  reason this was noticed before shipping rather than after.

- **The NVIDIA driver is offered on Arch and described elsewhere.** On Arch the
  module package name follows from the kernel package, so `prepare.sh` derives
  it and offers to install it, exactly as before. Debian builds the module with
  DKMS and Fedora with akmods, from packages whose names carry a driver branch
  that moves, and guessing one wrong leaves a machine with no graphics at all.
  Those two are told what to type. A script that refuses to install a graphics
  driver it cannot name with certainty is behaving correctly.

- **No NVIDIA package name is written into this project.** Both places that name
  one work it out on the machine instead, which is shorter than a table and
  cannot go stale. The 32 bit hint reads which package owns the 64 bit
  `libnvidia-encode.so.1` and puts this distribution's suffix on it — so it
  lands on `libnvidia-encode1:i386`, `xorg-x11-drv-nvidia-libs.i686` or
  `lib32-nvidia-utils` without being told any of them, and it cannot name the
  wrong driver branch because it is reading the branch already in use. The
  driver hint has nothing installed to read, so it asks the repositories which
  of the plausible names they carry and prints one that will resolve.

- **A repository that is not enabled is now said out loud.** When that search
  comes back with nothing, on both Debian and Fedora it means the NVIDIA
  packages are in a repository that is off by default, so the machine is told
  that non-free or RPM Fusion is not set up rather than being handed a package
  name that would not resolve. That is the more useful of the two answers and it
  was not available while the names were hard coded.

- **Whether the 32 bit driver userspace is present is asked of `ldconfig`**
  rather than of the package manager. What matters is whether the library is
  there for `nvidia-container-toolkit` to mirror into a seat, not which package
  put it there, and the three distributions name that package three ways while
  the library has one name everywhere.

- **The two prerequisites that need a repository the distribution does not ship
  are named before anything is installed.** Incus is in Debian 13 and not in 12;
  `nvidia-container-toolkit` is in neither. `polyseat-prepare` says where each
  comes from and stops, rather than adding a repository on somebody's behalf.
  Which repositories a machine trusts is not an installer's decision.

- **The `.deb` and the `.rpm` come from one description**,
  `packaging/nfpm.yaml`, built by `packaging/build-packages.sh` and attached to
  every release by a second job in the packaging workflow. The PKGBUILD is not
  generated from it and will not be: one builds from source on the machine
  installing it and the other describes a binary already built. What has to
  agree is the file list.

- **The update button offers the file that belongs to this host.** The daemon
  works out its own family in `internal/hostpkg`, looks for that one asset on
  the release, and installs it with that package manager. A host whose package
  manager is none of the three is told there is a newer version and is not
  offered a button that could not work, which is a different sentence from "that
  release has no package" and now says so.

- **`host/test-distro.sh` is new and CI runs it.** Eighty checks against stubbed
  package managers, needing no root, no network and nothing installed. It is
  what makes the two rows nobody here runs into something other than hopeful
  prose, and it is where the `dnf` dependency default was caught.

## 0.8.1

**A seat's Updates row said things that had stopped being true.** Two ways into
the same fault, both reported from a running machine within an hour of 0.8.0.

**Seats are not touched by this.** Nothing here changes how one is built, so an
existing machine updates with a restart and no seat has to be provisioned again.

- **An updated seat went on listing what it had just installed as waiting.** The
  update did the work, restarted the session, and never looked again, so the
  card kept drawing the reading taken before it until the six hour timer came
  round or somebody pressed the button. Three moments take a look and the third
  was added last and forgotten. They now go through one place that writes the
  answer and the time together, and the update reads the seat again rather than
  assuming: a package that did not upgrade, or one published in the minutes the
  update took, is still reported.

- **A started seat went on saying it was switched off.** Worse than the first,
  because it was never a finding. "The seat is not running" is a fact about the
  present, and a fact about the present goes out of date the moment the present
  moves. It is no longer written into a reading at all: the check answers with
  a refusal instead, which cannot outlive anything.

- **A stopped seat now keeps what was last found in it**, with the row saying
  how long ago. Old is not the same as wrong, and a usable answer was previously
  replaced with a note about the seat being off.

- **A seat that comes up having never been asked is asked shortly after**,
  rather than waiting out the rest of a six hour interval it spent switched off.
  In the background and one pass at a time, because the sweep that notices runs
  every ten seconds and must not do network work.

- **Both buttons appear only on a running seat.** Neither can do anything to a
  container that is not up, and Update software could previously be offered to a
  stopped seat on the strength of a reading from when it last ran.

- **Waiting looks like waiting.** Checking a seat spins inside its own button,
  which is where the wait belongs: the check reads one seat and leaves the rest
  of the interface usable, so it deliberately does not mark the card busy.
  Restarting the daemon takes the whole page instead, with a note on what a
  restart does to a seat and a reload when it is back — that one takes the API
  with it, so every card behind it stops being true and none of them know.
  Before this the cards stayed up and a pill in the corner said "reconnecting".

- **Coming back is not the same question as answering.** The restart is
  scheduled a second out through a transient systemd unit so the request is
  answered before the process dies, which means the first reply comes from the
  old process. A reload against that lands on a page whose daemon vanishes
  underneath it, so a success counts only once the daemon has been seen to go.
  After two minutes it stops waiting and says which commands to look with.

## 0.8.0

**A seat now says when the software inside it has fallen behind, and can be
brought up to date without being rebuilt.** Nothing was noticing this before.
Sunshine and the seat's distribution packages were installed while the seat was
provisioned and then left alone, so a seat built in one month kept that month's
Sunshine until some unrelated recipe change happened to rebuild it.

**Seats are not touched by this.** The provisioning recipe is unchanged, so an
existing machine updates with a restart and no seat has to be provisioned again.

- **The two kinds of behind are different and are now shown apart.** The
  existing "out of date, rebuild this seat" is `Generation`, a constant in this
  source, and it means the daemon's own recipe moved on. The new "Updates" row
  means Sunshine or the distribution published something since this seat was
  built. Rebuilding never noticed the second one, and there was nothing else
  that did.

- **What it costs to ask, and when.** Every six hours, one request to GitHub for
  the published Sunshine version shared across all seats, and a `pacman -Sy`
  inside each running seat against a *separate* database, which is what
  pacman-contrib's `checkupdates` does. The separate database is the point: a
  plain `-Sy` would leave the seat in the partial upgrade state that breaks an
  Arch system the next time anything is installed into it.

- **Never while somebody is playing.** The check is skipped for a seat being
  streamed from, because it pulls several megabytes over the same network the
  stream goes out on, and the update itself refuses outright rather than
  deferring: it restarts the session, and a button somebody pressed deserves an
  answer rather than a game ending minutes later.

- **An answer that never came is not evidence.** A seat is only offered an
  update where both versions are known. Without that guard a machine that cannot
  reach GitHub reports every seat as behind and offers to install the version
  already there, and a failed lookup is cached for the full interval so an
  offline host does not ask once per seat per sweep forever.

- **Asking is a button too, not only a timer.** "Check for updates" is on every
  built seat and answers straight away, because a six hour timer is right for
  noticing and wrong for a question somebody is currently asking. It drops the
  cached GitHub answer first, so "now" means the whole question rather than a
  fresh look at the seat compared against a version from five hours ago. It
  reads and changes nothing, which is why it is always available where the
  update itself is offered only when there is something to install.

- **The Updates row is there in every state.** It first appeared only when
  something was waiting, which left "nothing waiting" and "never asked" as the
  same blank space on the card. It now says which, and when the seat was last
  asked.

- **The update runs the provisioning steps rather than a second copy of them.**
  So a seat updated this way lands where a freshly provisioned one would, and
  the `--assume-installed` flags that keep a plain `pacman -Syu` from colliding
  with the injected NVIDIA driver stay fixed in the one place they already were.

## 0.7.1

**The resolution and the Streaming row did not move when somebody started,
paused or ended a stream.** They caught up whenever something else happened to
change, which on a quiet host could be minutes.

**Seats are not touched by this.** Nothing here changes how one is built, so an
existing machine updates with a restart and no seat has to be provisioned again.

- **A sweep pushes what it learned, not only what it renamed.** The page reloads
  on a pushed change and on nothing else, and the sweep decided whether to push
  by comparing the seat's *state* — which is precisely what does not move here.
  The seat was running before the stream and is running after it. What moved was
  the output, the session and whether somebody is connected, and none of the
  three was ever compared. The seats named as in use under the daemon's own
  restart button were stale for the same reason.

  So the reading is compared rather than the state: everything a sweep learns
  that the interface then shows, in one string. A field nobody remembers to
  compare is how this reads wrong, and a field added to the sweep is added to the
  comparison by being read at all. When the sweep is late, so is the whole card.

- **A reading that failed no longer blanks the resolution.** Reading the output
  answers with nothing for every failure it has, and that was written down
  unconditionally. Harmless while nothing was pushed; with this release it would
  have taken the resolution off the card on one exec that timed out and put it
  back ten seconds later. It is the rule the stream is already read under: a seat
  that was not asked successfully has not answered.

- **A stopped seat no longer claims a resolution.** That field is what the screen
  is running at *now*, so a seat that had been streaming at 2560x1600 went on
  saying so after it stopped. A container that is not running has no output, the
  same thing that is already said about its stream.

## 0.7.0

**A seat being built for the first time could not finish.** It stopped on the
shared library, and Flathub had quietly gone the same way four seconds before
that:

```
! Flathub could not be added, installing new software in this seat will not work yet
  error: mkdirat: Permission denied
! starting failed: library: taking over Steam's library folder: mkdir: cannot
  create directory '/home/player/.local/share/Steam/steamapps': Permission denied
```

**Seats have to be provisioned again.** The provisioning generation went up, so
the interface marks every existing seat stale and offers to build it again. That
is a few minutes per seat rather than a restart, and it is how a seat built while
this was broken gets repaired.

- **The player owns their own home again.** Setting Steam's default Proton wrote
  its configuration with `install -d -o uid -g uid` in front of it, and install
  applies the ownership it is given to the last component only. Every parent it
  had to create came out belonging to whoever ran it, which is the daemon, which
  is root — so `.local`, `.local/share` and `Steam` were root's, the player owned
  nothing but the leaf, and the two things in a seat that add something of their
  own beside what the daemon put there failed on the same permission at once.

  Only ever on a first build, which is why it went unseen for as long as it did.
  A seat that had already run Steam had those directories from Steam itself and
  there was nothing left for install to create.

  The call is gone rather than corrected. Writing the file already creates every
  missing directory through the Incus file API, and that carries the player's uid
  down all of them.

- **A seat built while it was broken is repaired when it is provisioned again**,
  before the flatpak step and before the library, by handing back the four
  directories on the way to Steam's configuration wherever they are not the
  player's already. Each one is named in the seat log as it is handed back.

  Not recursive, deliberately: those four are the whole of what the bug could
  create, everything written inside them went in through the file API with the
  right uid on it, and Steam's `steamapps` is where the shared pool is mounted,
  which belongs to the host and to every other seat sharing it.

  This is also why the generation went up rather than leaving it to the seats
  that visibly failed. With the shared library off, the step that stopped the
  build here returns early, so a seat could come up working and silently without
  Flathub, and would never have been provisioned again on its own.

## 0.6.3

**The daemon greyed out its own restart button on a host where nothing was
running.** Under the words *"Restart when nobody is playing"*, with one seat,
stopped, never built.

- **A seat that is not running is no longer counted as somebody playing.**
  `streamUnknown` is the zero value of the type, and the reading was only ever
  assigned inside the branch that runs when Sunshine is active, so a seat whose
  Sunshine was not up recorded "cannot tell" instead of "not streaming". Every
  seat passes through exactly that state: while one is being built its container
  runs and Sunshine does not.

  Then it latched. The poll returns before reading anything when the container
  is not running, so a seat that stopped in that state was never read again and
  went on saying "cannot tell" for the life of the daemon. Everything that must
  not disturb a stream treats that like a live one, which is right while a seat
  is up and wrong once it has stopped — so one seat whose build had failed was
  enough to block the restart, that seat's app list and its software management
  at the same time.

  A container that is not running has no stream, which is the one thing here
  that can be said without asking anybody, so stopping and disappearing both
  drop what was believed. And Sunshine not running is now an answer rather than
  a silence: the seat was asked and what it said settles the question. Only a
  seat that could not be asked at all leaves the last belief standing, which is
  the caution that was meant there and had been guarding the wrong case.

## 0.6.2

**A seat no longer stops being built because a mirror went quiet.** That was the
most common way for a build to fail and it never had anything to do with the
seat.

- **A package transaction is tried three times**, five seconds apart, with a line
  in the seat log each time so it is not done behind anybody's back. The failure
  it is for:

  ```
  error: failed retrieving file 'speexdsp-1.2.1-2-x86_64.pkg.tar.zst' from
  mirrors.kernel.org : Operation too slow. Less than 1 bytes/sec transferred
  the last 10 seconds
  warning: too many errors from mirrors.kernel.org, skipping for the remainder
  of this transaction
  error: failed to commit transaction (failed to retrieve some files)
  ```

  pacman is doing the right thing there — it gives up on a mirror that has gone
  quiet rather than hanging on it — and the transaction ends because the files
  it still wanted were only on that one. What followed was ten minutes of
  provisioning thrown away and a person pressing the button again. "For the
  remainder of this transaction" expires when the transaction does, so a second
  attempt gets a fresh choice of mirror.

  Only when the output looks like the network. A package that does not exist, a
  file another package owns, a signature that does not check out: those are
  answered once, because trying them three times would take three times as long
  to say the same thing. The three transactions that download go through it; the
  one that installs a file already on disk does not.

- **The uplink tests from 0.6.0 were reading this machine's network.** They fake
  the four questions the choice asks and it asks five: whether an interface is a
  port of a bridge went to the real `/sys`. They passed until a machine had
  `lan-bridge.sh` run on it, at which point the `enp4s0` they invent collided
  with a real card that had become a port of `br0`, dropped out of its own
  candidate list, and two tests quietly started proving something else. Both CI
  runs before this were green, because a GitHub runner has no `enp4s0`.

## 0.6.1

**The interface could be showing a state from minutes ago and had no way to
know.** `/api/state` went out with no cache headers at all, so a browser was
free to keep it — and Firefox did.

- **Every API reply now says `no-store`**, the same header the static files have
  carried all along. Without it the page read its whole picture of the machine
  out of a cached copy: the daemon found 0.6.0, logged it, and answered every
  "Check now" with the release, while the page went on saying *"It has not
  managed to ask yet"* from before its first check.

  The two halves disagreed because only one of them was live. A POST is never
  served from a cache, so the button reached the daemon every time and the
  journal showed a working check; the refresh that would have shown the answer
  came off disk. Nothing failed and nothing was logged, which is why this took a
  while to find — it was proved in the end from the page itself, by making the
  same request twice with `cache: "reload"` on the second.

  It made the update button do nothing at all, which is the symptom 0.4.3 had
  and an unrelated cause. A page open on any earlier version needs one reload
  with the cache bypassed, or a daemon carrying this fix, before it will show
  anything new again.

## 0.6.0

**A machine on wifi with an ethernet port can run Polyseat, and nothing has to
be typed for it.** Which is most machines: the port is all a seat needs, since
what it wants from the card is a segment to be a host on and not a way to the
internet.

**Seats are not touched by this.** Nothing here changes how one is built, so an
existing machine updates with a restart and no seat has to be provisioned again.

- **The uplink is chosen in one place**, `seat.Uplink`, and three things used to
  choose it separately. The daemon read `"uplink"` and fell back to the default
  route; `polyseatd -report` worked it out again in its own words;
  `host/lan-bridge.sh` read only the default route, under a comment claiming it
  read the uplink "the same way the daemon reads it, so the two cannot
  disagree". On a machine whose route out is wireless and whose seats hang off a
  wired card, they disagreed completely: the script worked on the wifi and
  refused.

  `polyseatd -uplink` prints the answer, with the reason on standard error, and
  needs neither root nor a running daemon. The script asks it and only works the
  question out itself when there is no binary to ask. A binary too old to know
  the flag is told apart from one saying there is no uplink, by the exit status,
  so an upgrade halfway through falls back rather than concluding the machine has
  no network.

- **A wired card with a cable in it is taken when the default route is
  wireless.** That is the whole feature above, and it happens only where there
  is one answer. Two wired cards with cables is a decision about which network
  the seats belong on, and guessing it produces seats Moonlight cannot see, so
  the interface names them and `"uplink"` settles it. A card with no cable is
  never chosen: a seat given one comes up, looks healthy and never gets an
  address.

- **Bridging works in that arrangement too.** The bridge is built over the
  seats' card rather than over whatever carries the route out; it is given
  `never-default`, so the machine keeps routing over wifi instead of quietly
  moving onto the card the seats hang off; and it is no longer judged on whether
  a gateway answers over it, which would have rolled a correct run back. What
  the daemon reads afterwards follows by itself, whichever way the uplink was
  decided: a configured interface is followed to the bridge it has become a port
  of, and an automatic choice sees the port drop out of the candidates while the
  bridge stays.

- **The interface says when the uplink is one no seat can use.** It showed the
  name and whether it was a bridge, which on a wireless machine reads as "your
  seats are isolated" when the truth is that no seat on it can have a network at
  all. 802.11 carries one MAC address per association, so a macvlan is refused by
  the driver and bridging would need 4-address mode at both ends, which ordinary
  access points do not do. `prepare.sh` has warned about this since it existed
  and `polyseatd -report` prints it; neither is where somebody looks after
  pressing a button that did nothing. The warning names the way out, and the
  state carries the reason as well as the name, because a host that hands its
  seats the ethernet port beside its wifi is doing something nobody typed.

- **The seat editor offers the bridge instead of pointing at another dialog.**
  The checkbox is no longer disabled on a plain interface. The preference is
  real and is stored; what a bridge decides is whether it does anything, and
  saying so is more honest than a box that cannot be ticked. Ticking it offers
  the button, which saves the seat first — the tick that led to pressing it
  would otherwise go with the dialog — and then hands the log to the panel that
  owns it rather than growing a second one.

## 0.5.0

**The uplink can be bridged from the interface.** It was a script, a terminal
and a warning to run it from a keyboard attached to the machine, and its last
word was a list of seats it had stopped and deliberately would not start again.
Both halves are gone. The button is in the host dialog under *The uplink*, and
the thing that starts a seat properly is the thing that presses it.

**Seats are not touched by this.** Nothing here changes how one is built, so an
existing machine updates with a restart and no seat has to be provisioned again.

- **Put the uplink on a bridge**, and take it off again, from the host dialog.
  It runs `host/lan-bridge.sh` as `polyseat-lan-bridge` and reports it a line at
  a time, the same way preparing a machine does and for the same reason: the
  script is the one copy of the procedure, it is where the rollback lives, and a
  second implementation of it in Go would mean one of the two is wrong and
  nobody knows which.

  `"web_lan_bridge": false` takes it off the page for a machine whose seats are
  not all for people in the same room. The interface password is asked every
  time regardless of `update_needs_password`, and the reason is not that this
  cannot be undone — it is the one host action that is undone by pressing the
  other button. It is where the page can be. A seat's own browser reaches this
  interface over the management bridge, so without that question a session
  opened inside a seat is a session that could hand that seat the LAN it was
  kept off.

- **The seats come back on their own, and after a failed run too.** The script
  stops every seat before it touches the interface, because the kernel refuses
  to make an interface a bridge port while a macvlan hangs off it, and a seat's
  macvlan counts even from inside its own network namespace. It then prints the
  names rather than starting them, because `incus start` brings a container up
  and leaves the compositor, Sunshine, the audio stack, the Moonlight app list
  and the wait for an encoder to somebody else.

  That somebody is the daemon, which is the whole reason this is worth a button
  rather than a shortcut for typing. It reads which seats are up before anything
  starts, and starts them again through its own start path when the run is over.
  Including when the run failed: a failed run has already stopped them and put
  the network back, and seats left down after a failure is the worse outcome
  rather than the safer one.

- **A bridged uplink survived a reboot by luck, and the luck has run out.** This
  is the fault behind "the internet was gone after a restart and I had to pick
  the right adapter by hand", and it was in the script rather than in the
  bridge.

  Most machines have no saved NetworkManager profile for their wired interface.
  NetworkManager invents one, keeps it in `/run`, and builds it again from
  scratch at every boot. The script wrote `connection.autoconnect no` to exactly
  that profile, with a comment explaining that this was to stop it racing the
  bridge port at the next boot. The comment described the intention correctly
  and the setting was thrown away with the file at shutdown. What came up next
  boot was two profiles wanting the same interface, and whichever won decided
  whether the address landed on the bridge or on the bare card.

  It is deleted now instead of switched off, which is what NetworkManager itself
  understands: a default wired connection that is deleted puts its device in
  `/var/lib/NetworkManager/no-auto-default.state` and is never invented again.
  Exactly one profile can claim the uplink after that. `--undo` writes a real
  saved profile in its place rather than waiting for the invented one to come
  back, the port connection carries a priority and unlimited retries so a switch
  that is not forwarding yet costs a second attempt rather than the network, and
  `--check` has gained the line that would have found this in the first place:
  how many profiles want the uplink at boot, where one is the only good answer.

- **`polyseat-lan-bridge` is placed by the checkout installer too**, into
  `/usr/local/bin`. Not tidiness: the daemon looks it up by name, `/usr/local`
  first and `/usr` second, and a daemon built from a checkout has no way to find
  the checkout it came from. The rule is the same one `polyseat-prepare` and
  `polyseat-uninstall` follow — a script goes in when something looks for it —
  and `polyseat-check-hardening` still does not, because nothing does.

## 0.4.3

**The install button in the interface had never worked once.** Everything else
here is what made that hard to see.

- **The daemon did not recognise its own package.** Every release publishes one
  file, `polyseat-x86_64.pkg.tar.zst`, and the name carries no version on
  purpose: `releases/latest/download/<name>` is a permanent link only while the
  name is, which is what keeps the documented `curl` command from ever having to
  be edited. The matcher in the daemon was written one release earlier, against
  the versioned name makepkg produces, and it was not brought along when the
  published name changed in 0.3.4.

  Nothing failed loudly. The interface said "that release has no package
  attached to it, so it cannot be installed from here", which reads like a fact
  about the release rather than a fault in the reader, and every machine has
  been told that about every release since.

  **This release still has to be installed by hand**, because the daemon doing
  the looking is the one with the fault. After it, the button works.

- **The tests agreed with the parser about a shape that had stopped existing.**
  The recording they used was made before the rename, so they proved the matcher
  could read an answer GitHub no longer gives. The recording is now today's, and
  there is a second test that reads `.github/workflows/package.yml` and holds the
  name it uploads against the name the daemon looks for. A recording proves what
  GitHub said once; only that proves what it says now.

- **The Updates panel carries the buttons**, rather than a sentence saying where
  else to press one. It was pointing at the line at the top of the page, which
  is behind the dialog the panel is in: being told to press something that the
  thing telling you is covering. Installing and restarting are both there now,
  in the same states the banner shows, decided in one place so the two cannot
  disagree.

## 0.4.2

Words and the one place that cannot be looked up again. Nothing here changes how
a seat is built, so updating is a restart of the daemon and no more.

- **The submenu is called *Host* rather than *Machine*.** It holds preparing
  this machine, checking for a newer Polyseat and removing it, which are the
  three things that are about the computer the seats run on rather than about a
  seat, and "host" is the word this project uses for that everywhere else. The
  entries under 0.4.0 and 0.4.1 below name it as it was called then.

- **The text pacman prints after installing says less and says where to go.**
  It carried the AMD warning and the shared library's filesystem requirement,
  both of which the interface says on its own in front of the thing they are
  about, and it named the address as `<this machine>`. It now prints the address
  a browser can actually open — the IPv4 address on the interface carrying the
  default route, the host name when there is no route to ask — on a line of its
  own with nothing after it, which is what makes a terminal treat it as a link.
  `host/install.sh` and `host/prepare.sh` end the same way and are shorter by
  about half.

## 0.4.1

Two things that 0.4.0 put one click too far away.

- **Preparing the machine was only behind the *Machine* button.** On a host that
  the daemon could reach Incus on — one where Incus was already installed and
  initialised — the page came up as the ordinary interface and the one thing a
  new installation still had to do was in a dialog nobody opens first. The panel
  is now on the page itself, above the seats, whenever the machine has not been
  prepared or has no seats yet, and it moves back behind the button once there
  is a seat.

  Whether the machine is ready is answered from one fact rather than from a
  marker file the daemon writes: whether root has an idmap range in
  `/etc/subuid` and `/etc/subgid`. Nothing else on an Arch machine writes that
  entry, without it every container start fails with a message that names
  neither subuid nor Polyseat, and it is two small file reads.

  The empty seat list says the same thing rather than offering to add one: a
  seat built on a machine in that state is a container that cannot start.

- **A button that asks GitHub now**, under *Machine*, instead of waiting up to
  six hours for the next look. It says when the daemon last managed to ask,
  because on a machine that has been off the network since yesterday "nothing
  newer" and "nothing heard" look identical and are not the same answer, and it
  says plainly when it found nothing rather than leaving a silence. It needs no
  password: looking is what `update_check` governs, and installing is still the
  button that asks for one.

## 0.4.0

**Both ends of Polyseat's life move into the interface.** Installing it was four
commands and removing it was a command plus a checkout; it is two commands and
two buttons now. What could not move is what a package may not do and what
nothing else can do for it: `pacman -U` and `systemctl enable --now polyseatd`.

**Seats are not touched by this.** Nothing here changes how one is built, so an
existing machine updates with a restart and no seat has to be provisioned
again.

- **The daemon comes up on a machine that is not ready.** It used to exit when
  it could not reach Incus, and on a machine that has just installed the package
  that is every time, because Incus is one of the things preparing it installs.
  systemd brought it back five seconds later and the interface that exists to
  explain exactly this was the one thing that never came up. It now serves a
  smaller page from the same address with the same certificate and the same
  password, offering the button that fixes it, and restarts into the ordinary
  interface by itself once Incus answers.

- **Prepare this machine**, in that page and under *Machine* afterwards, runs
  `host/prepare.sh` and reports it a line at a time. The same file somebody at a
  terminal runs as `polyseat-prepare`, because two of it would mean one of them
  is wrong and nobody knows which. The one thing it cannot work out for itself
  arrives from the browser: whose account goes in the `input` group, which
  `sudo` answers by itself and a daemon started at boot cannot.

- **Remove Polyseat**, under *Machine*, with the same three choices the command
  has: the daemon, the seats with it, the shared library with those. It asks for
  the interface password every time, whatever `update_needs_password` says, and
  deleting seats needs the word typed out. `"web_uninstall": false` turns the
  button off.

- **`host/uninstall.sh`, installed as `polyseat-uninstall`.** The removal used to
  live inside `install.sh`, which meant a machine installed from the package had
  no way to remove its seats without cloning the repository. Three things in it
  are not obvious: it runs from a copy of itself, because pacman deletes the file
  bash is reading; it removes the package with `-R` and not `-Rs`, because the
  `s` takes Incus with it on a machine where pacman pulled Incus in as a
  dependency; and it stops the daemon before touching anything it owns, which is
  the order that keeps Incus out of a "Stopping instance" that never finishes.

- **The readme's own removal command was that same trap.** It said
  `pacman -Rns polyseat`, which on such a machine removes Incus, bpftrace and
  python. `test-package.sh` now checks that Incus and bpftrace survive.

- **Nothing runs two pacman transactions at once.** Preparing refuses while an
  update is installing and the other way round, and both the restart button and
  the watcher that restarts the daemon by itself hold off while a prepare is
  running: `KillMode=mixed` means a restart takes the whole control group with
  it, and a pacman killed halfway leaves a lock and a partly applied
  transaction.

- **`prepare.sh` stopped assuming a terminal.** No colour where nothing renders
  it, `POLYSEAT_INPUT_USER` for the account, `POLYSEAT_FROM_DAEMON` for the
  closing "now start it" that is wrong when it is already running. It still
  refuses to install a graphics driver without somebody there to say yes, which
  is why a machine with no driver still ends at a terminal.

- **`test-package.sh` takes the browser's path.** Install the package, start the
  daemon with no Incus on the machine, claim it over HTTPS, prepare it through
  the API, wait for the daemon to restart into the real interface, and remove
  Polyseat from that interface. The two checks worth naming: the password chosen
  while the machine was not ready is still the password afterwards, and the
  removal left Incus and bpftrace installed.

## 0.3.5

A review pass, and most of what it found had been true for a while without
anybody meeting it. Nothing here changes how a seat is built.

- **The interface told every package installation that the udev rule was
  missing.** The rule that keeps seat input devices off the host desktop goes to
  `/etc/udev/rules.d` from a checkout and `/usr/lib/udev/rules.d` from the
  package, and the check looked only in `/etc`. So a package install carried a
  permanent warning about a protection that was in place and working, and
  advised running `host/install.sh`, which a package install does not have.
  `polyseat-check-hardening` had the same fault and told people to copy a file
  out of a checkout. Both now look where udev looks. A security warning that
  cries wolf is worse than none, because the next one is not read either.

  `test-package.sh` and `test-install.sh` each knew the right path for their own
  half, which is why this survived: they checked where the file was, and never
  what the daemon said about it.

- **A daemon whose web interface died exited 0**, so `Restart=on-failure` never
  fired and `systemctl status` said "inactive (dead)" rather than "failed". It
  looked like somebody had stopped it on purpose. It now exits with the failure.

- **Shutting down warned that the seat manager had not stopped in time**, on the
  one path where it had stopped first. Its result had already been taken out of
  a channel that holds one, so the wait afterwards sat for its full fifteen
  seconds and then complained.

- **`Unpair` and `ReloadApps` ignored what Sunshine answered.** A refusal
  arrives as a status in the body with a 200 beside it, which `Pair` checks and
  the other two decoded into a value nothing read. The interface could report a
  device removed while it was still paired. `internal/sunshine` had no tests at
  all and now has ten.

- **The update banner could name the wrong version.** It read the version out of
  whatever the checker currently offered, so a release published between
  installing and restarting made it claim that newer one was installed and
  waiting. The daemon now records what it actually installed.

- **The interface ran `pacman` on every state request**, about a tenth of a
  second each, to ask whether the package owns the running binary. That answer
  cannot change without a restart, so it is asked once at startup.

- **Reading the log of a seat that does not exist created a record for it**,
  which meant a map that grew for as long as somebody kept asking.

- **The `uhid` boot entry ships with Polyseat** instead of being written by
  `polyseat-prepare` into `/etc`. `/usr/lib/modules-load.d` from the package and
  `/usr/local/lib/modules-load.d` from a checkout, both of which systemd reads,
  which means whichever installed it also removes it. The old copy in `/etc` is
  taken out on the way past. That was the whole awkwardness 0.3.3 had to explain
  in its removal message, and the message is shorter for it.

## 0.3.4

The package standing on its own, which 0.3.3 assumed and did not arrange.

- **One file on a release page, not two.** 0.3.3 published the package twice,
  under makepkg's versioned name and under a name with no version in it, on the
  reasoning that the second is what makes `releases/latest/download/` a
  permanent link and the first is what tells two downloads apart. In practice it
  is a question asked of everybody who arrives at the page, and the release
  already says which version it is. Only the unversioned name is published now,
  and `pacman -Qi polyseat` says which version arrived.

- **`polyseat-lan-bridge` and `polyseat-check-hardening` are in the package.**
  The readme named `host/lan-bridge.sh` and `host/check-hardening.sh`, and the
  package shipped neither, so it named two files the machine did not have. That
  went unnoticed while the readme offered a checkout as an equal way in. Neither
  is a development aid: the first turns the uplink into a bridge, which is what
  local multiplayer between the host and a seat needs, and the second reports
  the console and device exposures. `docs/installation.md` now carries the
  table of which script ships under which name.

- **The readme describes one way in.** Installing from a checkout is how you run
  a particular commit rather than a release, which is a thing contributors and
  people testing unusual hardware do, and it is in
  [CONTRIBUTING.md](CONTRIBUTING.md) with the rest of what a working copy is
  for. `host/install.sh`, `host/update.sh` and `--purge` are unchanged and stay
  supported.

## 0.3.3

Two ways in and two ways to update, where before there was one of each. Nothing
here changes how a seat is built, so updating is a restart of the daemon and no
more, and existing seats are untouched.


- **Updating is a button in the interface**, where Polyseat was installed from
  the package. One button installs and a second restarts, and they are two
  because replacing the binary leaves the running process untouched: installing
  is safe in the middle of somebody's game and restarting is not. The restart is
  refused while anybody is streaming and names them, which is the one thing this
  does better than `host/update.sh`, since that script has to work out what this
  page already shows.

  **It reaches root on the host**, which nothing else in the interface does, and
  that is written up under its own heading in `docs/security.md` rather than
  folded into a feature list. `"web_update": false` turns it off.
  `"update_needs_password": true` makes it ask for the interface password at the
  moment the button is pressed, which is off by default and worth one specific
  thing: a page left open on an unlocked phone cannot be turned into a root
  installation by somebody who picks it up.

  The browser never says what to install. The request carries no address, no
  version and no file, and every one of those comes from the daemon's own pinned
  view of GitHub. An asset outside this project's own downloads is refused
  before anything is fetched, and what arrives is checked against the checksum
  the release states, which catches a download that went wrong and not one that
  was meant to.

- **The built package is attached to every release**, so installing is
  `pacman -U` on a file rather than a clone and a build, removing is
  `pacman -Rns`, and upgrading is the same `pacman -U` again. Not the AUR, where
  accounts still cannot be registered, and it matters less than it sounds: the
  AUR distributes recipes, and installing from one means building it yourself,
  which `host/install.sh` already does and does better.

  Downloaded first and installed second, which is two commands rather than
  one because the packages carry no signature: `pacman -U` on a URL applies
  `RemoteFileSigLevel`, which is `Required` by default and looks for a `.sig`
  that is not there, while a file already on disk applies `LocalFileSigLevel`,
  which Arch ships as `Optional`. Signing them is the fix and needs a key that
  is published and trusted by hand, which is the same key a package repository
  would need. The daemon's own update button is unaffected: it downloads the
  file before it installs it, so it was always taking the local path.

  The install URL carries no version and never will, and the file is published
  under a name that carries none either, because `releases/latest/download/` is
  a permanent link only while the file name is permanent. A documented command
  that has to be edited at every release is one that is eventually wrong. Which
  version a download turned out to be is inside the file, where pacman reads it
  from and where `pacman -Qip` shows it.

- **`--uninstall` now removes `/etc/modules-load.d/polyseat.conf`**, the file
  0.3.2 started writing. It is host configuration this installer put in `/etc`,
  the same as the udev rule beside it, and leaving it meant a machine that goes
  on loading a module at every boot for something no longer on it. The module
  itself is left loaded: unloading it would reach past this installation, since
  uhid is also what bluez uses for HID over GATT. `pacman -Rns` cannot do the
  same, because `polyseat-prepare` wrote the file and the package does not own
  it, so the removal message names it and gives the line that takes it out.

- **CI parses the interface.** Sixty kilobytes of JavaScript were checked by
  nothing: a typo in it fails no build, no test and no vet, only somebody's
  browser, as a blank page with the reason in a console nobody has open.

## 0.3.2

One bug, on hosts where uhid is a module rather than built in, which is most of
them. Like the one before it, it happens at boot and looks like anything but
what it is.

Seats are untouched by this one. Nothing in it changes how a seat is built, so
updating is a rebuild and a restart of the daemon and no more.

- **The uhid observer restarted every thirty seconds, forever, on a machine
  where nothing had yet used a gamepad.** The observer attaches a kprobe to
  `uhid_dev_create2` so that a pad can be attributed to the container that
  created it as a fact rather than a guess. A kprobe attaches to a symbol the
  running kernel has, and where uhid is a module its symbols do not exist until
  it is loaded. Nothing loads it in time: `/dev/uhid` is a static node declared
  in `modules.devname` and created at boot by systemd-tmpfiles, and the module
  is autoloaded only when something first opens that node, which is the first
  seat that runs a gamepad. So the daemon starts seconds into boot, finds no
  symbol, and retries until somebody happens to plug in a pad.

  `host/prepare.sh` now loads uhid and writes `/etc/modules-load.d/polyseat.conf`
  so that it is loaded at the next boot too, which is the boot that matters.
  `sudo host/install.sh` is enough to pick this up.

  Worth describing because of how well it hides. The node exists either way, so
  every check passed and everything looked correctly installed, and the one
  place that would have said otherwise, the warning that reads "load the uhid
  module", tests for the node and therefore could never fire on the machine it
  was written for. Found on CachyOS 7.2.0, where `CONFIG_UHID=m`. It was never
  seen on the development machine because bluez had already loaded uhid there
  for HID over GATT.

  Nothing was broken while this was happening. Gamepads worked; the broker fell
  back to attributing them by name, which is the documented fallback and a
  heuristic rather than a fact.

- **A supervised helper can now be given up on.** `supervise.Failed` has existed
  and been documented since the daemon was written, and was set nowhere, so the
  supervisor could not stop trying. A helper may now name the exits that will
  still be true in thirty seconds, and the observer names the one that means
  this kernel has no symbol to watch. It says so once, in the log and in the
  interface, and stops. Brokers are unchanged and still retry everything, which
  is right for them: most ways a broker dies are ways it might not die next
  time.

  The interface now tells "gave up" and "not running" apart, because they want
  different sentences and the first one names a cause and a command.

## 0.3.1

One bug, on NVIDIA hosts, of the kind that is worth a release on its own
because it only happens at boot and looks like anything but what it is.

Seats are untouched by this one. Nothing in it changes how a seat is built, so
updating is a rebuild and a restart of the daemon and no more.

- **A seat started right after boot could come up with no GPU at all.** The
  card's device nodes are not made at boot; the driver makes them when something
  first opens the card, and on a machine that autostarts seats twelve seconds in
  that something is the container start itself. libnvidia-container mirrors the
  host's `/dev/nvidia*` into the seat and mirrors what exists at the instant it
  looks, so it lost a race it had started: `/dev/nvidiactl` was created two
  milliseconds before `/dev/nvidia0`, and both seats on this machine got the
  first and not the second. The daemon now makes sure the nodes are there before
  it starts a container, and refuses to start one where they cannot be made.

  Worth describing because of how it looked rather than what it was. The
  container ran, every library was in place, and nothing said the card was
  missing: `nvidia-smi` inside the seat answered "No devices found", Sway could
  not make a renderer, and its unit restarted 272 times. It affects NVIDIA hosts
  only. If a seat has ever come up without a picture after a reboot, this was
  probably it, and stopping and starting that seat was the cure.

## 0.3.0

Two ways in, and everything a stranger needs to install it and to tell us when
it goes wrong.

Seats are untouched by this one. Nothing in it changes how a seat is built, so
updating is a rebuild and a restart of the daemon and no more.

- **`sudo polyseatd -report` describes a whole installation in one go**, for a
  bug report: version, distribution, card and driver, Incus, whether the library
  filesystem can really share blocks, the uplink, every seat and which recipe
  built it, and the last 200 journal lines. It runs without the daemon, which is
  the point, because it is wanted most on a machine where the daemon will not
  start. It reads a machine and changes nothing, and it opens no password, key
  or certificate.
- **A seat record on disk says which layout it is in**, and a build refuses one
  it does not understand rather than reading it anyway. Records written before
  this field are the same layout by definition and keep working untouched. It is
  here now because a field like this is worth nothing added later: it cannot say
  anything about the records that came before it.
- **The interface says so on an AMD machine**, since that path has never been
  run on real hardware by its author and whoever opens the page has quite
  possibly not read the readme.
- `CONTRIBUTING.md`, `SECURITY.md` and issue forms that ask for
  `sudo polyseatd -report` first. Security problems have a private channel now
  rather than a public issue.
- **There is an Arch package**, in `packaging/aur/PKGBUILD`, built and tested
  against a fresh machine on every release. It is **not published**: new AUR
  accounts cannot be registered at present, so there is no account to upload it
  from, and this entry said it installs from the AUR before that was known.
  Nothing about the package is waiting on work. The installer is now two halves
  because of it, and that half stands on its own: only one of them can be
  packaged, since an Arch package may place files and may not initialise Incus,
  write to `/etc/subuid` or add an account to a group. That half is
  `host/prepare.sh`,
  which the package installs as `polyseat-prepare` and asks for after
  installing. The checkout install runs it for you and is otherwise unchanged.
- The daemon finds its input helpers under `/usr/local/lib` or `/usr/lib`
  without being told which, since the same binary is installed both ways.
- Continuous integration on every push: build, vet, gofmt, the tests, and
  `shellcheck` over the shell scripts. It makes itself a btrfs filesystem in a
  loopback file first, because a runner's disk cannot share blocks and the
  library tests would otherwise skip in silence and report green.
- `host/test-package.sh` builds the package and installs it on a fresh virtual
  machine, the way `host/test-install.sh` does for the checkout. It also checks
  the three things a package must not do.

## 0.2.0

Noticing that there is a newer version, and a way to take it.

Seats are untouched by this one. Nothing in it changes how a seat is built, so
updating to it is a rebuild and a restart of the daemon and no more. Anybody on
0.1.0 has to do that update by hand, since `host/update.sh` arrives with this
version rather than before it:

```
git fetch --tags && git checkout v0.2.0 && sudo host/install.sh
```

- **The interface says when a newer Polyseat has been published.** One request
  to GitHub every six hours, a line at the top when there is something to say,
  and nothing else: it never downloads and never installs. It sends nothing
  about the machine, and `"update_check": false` in the configuration turns it
  off, after which no request is made at all.
- Only a build sitting exactly on a release tag is told anything. A build from
  an untagged commit cannot be compared with a release, and being told to
  "update" to something older than what is running is worse than silence.
- **`host/update.sh`** does the update: fetch, check out the newest release, run
  the installer. It refuses a checkout with uncommitted work in it rather than
  stashing or forcing, and it waits for a moment when nobody is streaming,
  because installing restarts the daemon and that takes every seat's input
  broker with it. `--check` looks without changing anything, `--now` skips the
  waiting, `--tag` goes to a particular release.
- Doing it by hand is unchanged and still documented.

## 0.1.0

The first release, and the state the project was in when it stopped being an
experiment. Everything here has been run on the machine it was written on: one
Arch host with an RTX 4080, seats streaming 4K to Apple TVs over Moonlight,
installed and uninstalled from scratch once to make sure the instructions are
the instructions.

### Seats

- One click takes a seat from nothing to a running session: an Incus system
  container with headless Sway, its own Sunshine, its own PipeWire and its own
  Steam account, on a macvlan of its own so it is a host on the LAN and can use
  the standard Sunshine ports.
- The NVIDIA userspace that the container toolkit leaves incomplete is repaired
  during provisioning. Without it a seat comes up, streams and looks healthy
  while encoding on the CPU.
- AMD is built differently and more simply: Mesa and `vulkan-radeon` are
  ordinary packages inside the seat, nothing is injected across the container
  boundary, and Sunshine encodes with VA-API. **Never run on real hardware.**
  What was verified and what was not is in [`docs/amd.md`](docs/amd.md).
- Seats built by an older recipe are recognised as such. The interface names
  them and offers one button that brings every one of them forward.
- Pairing happens in the Polyseat interface for every seat, rather than in one
  Sunshine page per seat.

### Input

- Each client's keyboard, mouse and gamepad reach that client's session and
  nothing else, and the host desktop sees none of them.
- Ownership is decided structurally, from what the kernel says created a
  device, not from the name the device claims. The seat log says for every
  device which of the two it was.
- A gamepad is enough to use a seat: an on-screen keyboard, and a pointer on the
  sticks that turns itself on when the desktop is in front and hands the
  controller back to a fullscreen game. Its speed is a slider that takes effect
  while somebody is holding the pad.

### The shared library

- A game installed once is playable in every seat without being downloaded
  again. Each seat keeps its own fully writable copy and the copies share their
  blocks on disk through reflinks. Taking this machine's 69 GB library into the
  pool took 0.8 seconds and 432 KB.
- The pool is the seat's only Steam library, so there is nothing to choose in
  the install dialog and nothing to get wrong.
- The host's own Steam library is a member of the pool, both as a source and as
  a destination.
- Launchers other than Steam use `shared/`, one folder per game, which works for
  Heroic, Lutris and Bottles alike.
- Requires a filesystem that can share blocks. btrfs and XFS with `reflink=1`
  can, ext4 cannot. The daemon finds out by cloning a block rather than by
  trusting the filesystem's name, and says plainly when the answer is no. Seats
  work either way.

### The desktop in a seat

- Connecting lands on a desktop with a launcher, a bar and a file manager.
- Moonlight's app list and the seat's own launcher are generated from the same
  scan of what is really installed, with box art, so a game is in both menus
  without anybody making a shortcut.
- Software goes in from either end without a password and without root: from
  inside the seat, or from the Polyseat interface with a progress bar. AppImages
  count, which matters because many emulators are published no other way.
- Every seat carries Proton CachyOS beside Valve's own, from the project's
  GitHub releases rather than from a package repository, set as the default
  compatibility tool. It updates itself and waits for a seat that is neither
  streaming nor holding the files open.

### Picture and latency

- The seat's output is virtual, so it becomes the size and refresh rate each
  client asks for.
- The framerate is capped from outside instead of by turning vsync on: games
  stay uncapped and pay no vsync latency. Measured in a seat, 14866 fps uncapped
  becomes 60.00 fps at 0.03 ms of frametime jitter, against 0.40 ms for vsync.
- Measured from an Apple TV over wifi, one seat, 4K HEVC at 60: host processing
  latency 3.9 ms average, no frames dropped by the network.

### Host

- `host/install.sh` sets up a fresh machine: packages, the idmap range every
  container start needs, Incus initialised if nobody has, the daemon, the input
  helpers, the udev rule and one systemd unit. Tested against a fresh VM by
  `host/test-install.sh`.
- `--uninstall` leaves the seats alone, `--purge` takes them along and asks
  first, `--library` also removes the shared games. `host/reset-machine.sh` puts
  the machine back the way it was.
- `host/lan-bridge.sh` turns the uplink into a bridge, which is what local
  multiplayer between the host and a seat needs. It rolls back completely on any
  failure, because the first version of it took this machine off the network.
- Whether a particular seat can reach the host is a checkbox on its card.
- The interface is password protected, TLS only, and the first password is
  chosen by whoever opens the page first rather than generated into a log.

### Known limits

- **Arch based hosts only.** The installer queries pacman rather than pretending
  to be portable.
- **The AMD path has never run on real hardware.**
- **A host NVIDIA driver update needs seats to be provisioned again**, or
  Sunshine in them falls back to the software encoder. This does not apply to
  AMD, where the driver is a package inside the seat.
- **Nothing checks for a newer Polyseat.** Updating is checking out the tag and
  running the installer again.
- **A deleted seat's game directory stays on disk** and nothing in the interface
  shows it. That is deliberate, so nobody's games vanish quietly, but it means
  the space has to be found by hand.
- **Licences are not shared and cannot be.** Files being present does not mean a
  seat's Steam account may run them.

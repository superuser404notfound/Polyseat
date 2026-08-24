# Changelog

A version is a git tag, and the daemon is stamped with the tag it was built
from. `polyseatd -version`, the line at the bottom of the web interface and the
first line in the journal all answer with the same string, so there is never a
question of which build is running.

Versions are `MAJOR.MINOR.PATCH`. Before 1.0 the minor number carries anything
that changes behaviour, including changes that need seats to be built again.
When that happens it is written here, because it is the one kind of update that
costs a few minutes per seat rather than a restart.

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

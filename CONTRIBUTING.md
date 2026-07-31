# Contributing

The most useful thing anybody can send this project right now is **a report from
a machine that is not the one it was written on**. One Arch host with an RTX
4080 has seen every line of this work. Whether it works on yours is genuinely
unknown, and that is true for AMD cards in particular, where the whole path was
written and reasoned about without a card to run it on.

```
sudo polyseatd -report
```

That describes the machine in one go and is what to paste into an issue, whether
something broke or nothing did. Read it first: it carries your host name, your
seat names and their private addresses, and it says so at the top.

## Running the tests

```
go test ./...
```

Most of it needs nothing special. Two things do, and they **skip loudly rather
than pass quietly**, so read what a run says it skipped:

- The library tests need a filesystem that can share blocks. `/tmp` is usually
  tmpfs and cannot. Point `POLYSEAT_TEST_DIR` at a directory on btrfs, or on XFS
  made with `reflink=1`.
- Some seat tests run the real Python helpers and need `python3`, `evdev`,
  `librsvg` and Pillow.

`.github/workflows/ci.yml` sets all of that up on a runner, including half a
gigabyte of btrfs in a loopback file, and is the shortest description of what a
full run wants.

## What the code looks like here

Read a file before adding to it; the conventions are visible in every one of
them. The ones worth stating anyway:

- **Everything in the repository is English.** Code, comments, commits,
  documentation.
- **No em dashes.**
- **Polyseat is capitalised in prose**, `polyseatd` and the other technical names
  are not.
- **Comments say why, not what.** The code already says what it does. A comment
  earns its place by recording what was measured, what was tried and rejected,
  or what will look wrong to the next reader and is not. Several of the longer
  ones in here are the whole reason a subtle thing has not been undone twice.
- Commit messages are prose, and they carry the same reasoning. What changed is
  in the diff.

## What a test has to do here

**Break every new check once on purpose and watch it fail.** A guard that has
never been seen to fire has not been tested, and this project has shipped
several that could never have fired at all: a probe of block sharing that
answered yes on a full copy, an isolation check made with a tool that cannot see
gamepads, a nil guard against a value that was never nil. Each of those passed.

Two habits follow from that:

- **Make test data from the real source.** A fixture written by hand agrees with
  whatever the parser expects. The release parser is tested against a recording
  of what GitHub really answered; the Steam config parser against real files from
  real seats, with the values anonymised.
- **A second check that measures nothing is worse than no second check**,
  because it reads as confirmation. If two checks can be fooled by the same
  mistake, they are one check.

## Pull requests

CI has to pass: build, vet, `gofmt`, tests, `bash -n` and
`shellcheck --severity=warning` over the shell scripts.

Small and self-contained gets read quickly. A large change is more welcome as an
issue first, describing what you want to do, because the answer may be that it
conflicts with something written down in [docs/architecture.md](docs/architecture.md)
or [docs/security.md](docs/security.md), and finding that out after the work is
nobody's idea of a good afternoon.

Anything touching how a seat is built has to raise `Generation` in
`internal/seat/provision.go`. That is what tells existing seats they are behind
and offers to rebuild them; without it, a change reaches new seats only and the
old ones quietly differ.

Anything changing what a seat record on disk *means* has to raise `RecordSchema`
in `internal/seat/seat.go`. Adding a field does not: a decoder ignores what it
does not know, and a missing field is the zero value, which is what every
optional field there already means.

## Licence

GPL-3.0-or-later, like the rest of the project. By contributing you agree your
work is published under it.

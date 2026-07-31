# Security

## Reporting something

Use GitHub's **Report a vulnerability** button under the Security tab of this
repository. It opens a private thread with the maintainer, and it exists so that
nothing has to be described in a public issue first.

This is a project run by one person in their spare time. There is no schedule to
promise and none will be invented here: expect an answer within a week, and say
so in the thread if a week passes.

## What this is, before you decide something is a bug

Polyseat's threat model is written down in
[docs/security.md](docs/security.md), and it is deliberately narrow: **several
people trusted by the owner, playing on one machine on a home network.** It is
not built to hand a seat to a stranger over the internet, and the document says
so on its first page.

That document also lists, with reasons, what is deliberately accepted. Among
them: the interface answers on the whole network, seats sit directly on the LAN,
a seat can install its own software, an AppImage in a seat is not sandboxed, and
the kernel console stays reachable. Those are choices with the reasoning
attached, not oversights, and a report that one of them exists is a report of
something already known.

**What is worth reporting** is anything that breaks a line the project claims to
hold. The clearest examples:

- A seat reaching another seat's input devices, session, or files.
- Anything reachable without the interface's password that should not be.
- A seat reaching the host when its own switch says it cannot.
- Secrets appearing where they should not: in a log, in the state directory with
  the wrong mode, in `polyseatd -report`, in the interface without a session.
- The input broker being made to do something by a name or a device coming out
  of a container, which is data a seat controls.

The verified half of docs/security.md is the list of things claimed to hold, and
each entry says how it was measured. If a measurement there does not reproduce
on your machine, that is worth a report on its own.

## Versions

There is one supported version and it is the newest release. This project is
young enough that backporting a fix to an older tag would cost more than saying
so plainly.

`polyseatd -version` and the foot of the web interface say which version is
running; please include it, or the whole of `sudo polyseatd -report`, which says
it along with everything else about the machine.

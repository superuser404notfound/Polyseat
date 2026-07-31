#!/usr/bin/env bash
# Updates Polyseat to the newest published release.
#
# Everything this does can be done by hand, and by hand is still fine:
#
#   git fetch --tags && git checkout v0.2.0 && sudo host/install.sh
#
# What this adds is the two things that are easy to get wrong on a machine other
# people play on. It refuses to touch a checkout with work in it, and it waits
# for a moment when nobody is streaming, because installing restarts the daemon
# and that takes every seat's input broker with it.
#
#   sudo ./update.sh           update to the newest release, asking first
#   sudo ./update.sh --check   say what is available and change nothing
#   sudo ./update.sh --tag X   go to a particular tag, older ones included
#   sudo ./update.sh --now     do not wait for the seats to be idle
#   sudo ./update.sh --yes     do not ask
#
# --check needs no root and changes nothing at all, not even a fetch.
set -euo pipefail

HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd -- "$HERE/.." && pwd)"

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

check_only=false
assume_yes=false
wait_for_idle=true
want_tag=

while [[ $# -gt 0 ]]; do
    case "$1" in
        --check) check_only=true ;;
        --yes|-y) assume_yes=true ;;
        --now) wait_for_idle=false ;;
        --tag) shift; want_tag=${1:-} ;;
        *) echo "unknown option: $1"; exit 1 ;;
    esac
    shift
done

# git as root over a repository owned by somebody else is the case git refuses
# by default. It makes an exception when sudo says whose it is, which covers the
# normal way this is run, and this covers the rest.
git_() { git -C "$REPO" -c safe.directory="$REPO" "$@"; }

step "This checkout"

if ! git_ rev-parse --git-dir >/dev/null 2>&1; then
    bad "$REPO is not a git checkout, so there is nothing to update from"
    echo "    Downloaded as an archive? Clone it instead, then this works:"
    echo "    git clone https://github.com/superuser404notfound/Polyseat.git"
    exit 1
fi

current=$(git_ describe --tags --always --dirty)
ok "at $current"

# Refused rather than stashed or forced. An uncommitted change in here is
# somebody's work, and a script that runs as root and updates a daemon is not
# the place to decide what happens to it.
if [[ -n "$(git_ status --porcelain)" ]]; then
    bad "there are uncommitted changes in $REPO"
    echo "    Commit or stash them first. This script will not decide what"
    echo "    happens to them for you."
    exit 1
fi

step "Published releases"

if [[ $check_only == false ]]; then
    [[ $EUID -eq 0 ]] || { bad "needs root, because installing does"; exit 1; }
fi

# --check reads what is already here and asks nobody. An update that has to
# fetch first is an update, and this option exists for the case where somebody
# wants to know without changing anything, including the network.
if [[ $check_only == false ]]; then
    git_ fetch --tags --quiet
    ok "fetched"
fi

# The same rule the daemon applies in internal/update: a release is a tag of
# exactly three numbers. Anything else, including a release candidate, is not
# something to be moved onto by a script that then restarts the daemon.
#
# --sort=-v:refname is git's own version sort, which compares the numbers as
# numbers. Sorted as text, v0.10.0 would come out older than v0.9.0.
newest=$(git_ tag --list --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)

if [[ -z $newest ]]; then
    bad "no release tags here"
    echo "    Nothing has been published yet, or this clone has no tags."
    exit 1
fi

target=${want_tag:-$newest}

if [[ -n $want_tag ]]; then
    if ! git_ rev-parse -q --verify "refs/tags/$want_tag" >/dev/null; then
        bad "there is no tag $want_tag"
        exit 1
    fi

    ok "newest is $newest, going to $target because you asked for it"
else
    ok "newest is $newest"
fi

# Compared by what the commits are, not by what the strings are. Checking out a
# tag leaves HEAD detached at that commit, so after an update `describe` answers
# with the tag and this is what recognises that as "nothing to do".
#
# The second test is the one that matters on a machine somebody develops on.
# Without it, a checkout of main sitting a few commits after the newest tag is
# not equal to it, so this would offer to "update", check the tag out and move
# the machine backwards. A tag whose commit is already an ancestor of HEAD is
# something this checkout has, not something it is missing.
already=false
if [[ "$(git_ rev-parse HEAD)" == "$(git_ rev-parse "$target^{commit}")" ]]; then
    already=true
fi

# Not applied when a tag was named. Asking for one by name is asking to go
# there, and going back to an older release on purpose is the reason --tag
# exists at all.
ahead=false
if [[ $already == false && -z $want_tag ]] && git_ merge-base --is-ancestor "$target^{commit}" HEAD; then
    ahead=true
fi

if [[ $already == true || $ahead == true ]]; then
    if [[ $ahead == true ]]; then
        ok "this checkout is ahead of $target, at $current"
    else
        ok "this checkout is already $target"
    fi

    if [[ $check_only == true ]]; then
        exit 0
    fi

    echo "    Nothing to update to. To rebuild and restart what is here:"
    echo "    sudo $HERE/install.sh"

    if [[ $ahead == true ]]; then
        echo "    To go back to the release anyway: sudo $HERE/update.sh --tag $target"
    fi

    exit 0
fi

if [[ $check_only == true ]]; then
    step "Available"
    echo "  $current is installed here, $target has been published."
    echo
    echo "  What changed:"
    echo "  https://github.com/superuser404notfound/Polyseat/releases/tag/$target"
    echo
    echo "  To take it: sudo $HERE/update.sh"
    exit 0
fi

step "Seats"

# Whether anybody is streaming, asked of the sockets Sunshine opens for a
# session rather than of a connection.
#
# The UDP ports exist for as long as the session does and belong to the running
# process, so they cannot go stale the way a file can. The TCP connection on
# 47989 is not the same question and has been seen to read as absent right
# through a stream, which is how the daemon once decided an occupied seat was
# empty.
streaming() {
    local row name state rows=() found=()

    # The list is read out in full before any of it is used, and the exec below
    # gets its stdin from /dev/null.
    #
    # Both because of the same thing, which cost an hour: `incus exec` reads
    # standard input, and in a `while read` loop fed by the list, standard input
    # is the list. The first exec swallowed the rest of it, so only the first
    # seat was ever looked at. With a session open on the second seat this
    # function reported that nobody was playing, which is the exact failure it
    # exists to prevent.
    mapfile -t rows < <(incus list -c ns -f csv 2>/dev/null || true)

    for row in "${rows[@]}"; do
        IFS=, read -r name state <<<"$row"

        [[ $state == RUNNING ]] || continue

        if incus exec "$name" -- ss -H -uln </dev/null 2>/dev/null |
            grep -qE ':(47998|47999|48000)( |$)'; then
            found+=("$name")
        fi
    done

    ((${#found[@]})) && printf '%s\n' "${found[@]}"

    return 0
}

busy=$(streaming)

if [[ -n $busy ]] && [[ $wait_for_idle == false ]]; then
    warn "streaming right now: $(echo "$busy" | tr '\n' ' ')"
    echo "    Going ahead anyway, because --now. Their controllers will drop"
    echo "    for a moment when the daemon restarts."
elif [[ -n $busy ]]; then
    warn "streaming right now: $(echo "$busy" | tr '\n' ' ')"
    echo "    Waiting for that to end. Ctrl-C to stop waiting, or run again"
    echo "    with --now to go ahead regardless."

    while [[ -n $busy ]]; do
        sleep 30
        busy=$(streaming)
    done

    ok "nobody is streaming any more"
else
    ok "nobody is streaming"
fi

if [[ $assume_yes == false ]]; then
    step "Update"
    echo "  $current becomes $target, and the daemon is rebuilt and restarted."
    echo "  https://github.com/superuser404notfound/Polyseat/releases/tag/$target"
    echo
    read -r -p "  Go ahead? [y/N] " answer
    [[ $answer == [yY] ]] || { echo "  Left alone."; exit 0; }
fi

step "Checking out $target"

# The branch that was here is remembered out loud rather than restored later.
# The checkout leaves HEAD detached at the tag, which is the right place for a
# machine that runs a release, and somebody who was on a branch should be told
# where to go back to rather than have it happen behind them.
was=$(git_ rev-parse --abbrev-ref HEAD)

git_ checkout --quiet "$target"
ok "now at $(git_ describe --tags)"

if [[ $was != HEAD ]]; then
    echo "    You were on $was. Back to it with: git -C $REPO checkout $was"
fi

# Handed over rather than repeated. install.sh is what knows how to build this,
# where every file goes and when to restart the daemon, and a second copy of any
# of that here would be a second copy to keep right.
exec "$HERE/install.sh"

# shellcheck shell=bash
# shellcheck disable=SC2034  # sourced: what looks unused here is used by the callers
# Shared definitions for the M1 scripts.
CT="${CT:-seat1}"                  # name of the Incus container
SEAT="${SEAT:-seat1}"              # seat tag inside the device name
PAD_NAME_PREFIX="polyseat:${SEAT}"
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

# After `usermod -aG incus-admin` the running session is still missing the
# group until the next login. Rather than reminding people: re-exec once under
# the group.
if ! id -nG | tr ' ' '\n' | grep -qx incus-admin \
   && getent group incus-admin | grep -qw "$USER"; then
    exec sg incus-admin -c "$(printf '%q ' "$0" "$@")"
fi

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

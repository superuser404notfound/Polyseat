# gemeinsame Definitionen für die M0-Skripte
CT="${CT:-seat1}"
SEAT="${SEAT:-seat1}"
PAD_NAME_PREFIX="polyseat:${SEAT}"
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

# Nach `usermod -aG incus-admin` fehlt die Gruppe der laufenden Session bis zur
# nächsten Anmeldung. Statt daran zu erinnern: einmal unter der Gruppe neu starten.
if ! id -nG | tr ' ' '\n' | grep -qx incus-admin \
   && getent group incus-admin | grep -qw "$USER"; then
    exec sg incus-admin -c "$(printf '%q ' "$0" "$@")"
fi

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

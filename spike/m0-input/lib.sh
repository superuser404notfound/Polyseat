# gemeinsame Definitionen für die M0-Skripte
CT="${CT:-m0}"                     # Name des Incus-Containers
SEAT="${SEAT:-m0}"                 # Seat-Tag im Gerätenamen
PAD_NAME_PREFIX="polyseat:${SEAT}"
HERE="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
step() { printf '\n\033[1m%s\033[0m\n' "$*"; }

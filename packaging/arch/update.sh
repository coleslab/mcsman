#!/usr/bin/env bash
# Update an installed mcsman package from this checkout (see README,
# "Updating mcsman"). Run as your normal user from anywhere; it uses sudo for
# the service, pacman and copying the database.
#
#   update.sh [-u USER] [-d SECONDS] [-b] [-n] [-y]
#   update.sh --rollback DIR
set -euo pipefail

usage() {
	cat <<'EOF'
Usage: update.sh [options]
       update.sh --rollback DIR

  -u USER   service user (mcsman@USER); default: the running instance, else you
  -d SECS   in-game countdown before stopping running servers (default 60)
  -b        take a backup of each running server after stopping it
  -n        don't git pull; build the current commit
  -y        don't ask for confirmation
  --rollback DIR
            reinstall the package and database saved in DIR by an earlier run
EOF
}

die() { echo "update.sh: $*" >&2; exit 1; }
say() { printf '==> %s\n' "$*"; }

svc_user= delay=60 backup=0 pull=1 yes=0 rollback=
while [[ $# -gt 0 ]]; do
	case $1 in
	-u) svc_user=${2:?}; shift ;;
	-d) delay=${2:?}; shift ;;
	-b) backup=1 ;;
	-n) pull=0 ;;
	-y) yes=1 ;;
	--rollback) rollback=${2:?}; shift ;;
	-h | --help) usage; exit 0 ;;
	*) usage >&2; exit 2 ;;
	esac
	shift
done

[[ $EUID -ne 0 ]] || die "run as your normal user (makepkg refuses root)"
[[ $delay =~ ^[0-9]+$ ]] || die "-d needs a number of seconds"
for c in mcsctl jq makepkg pacman; do
	command -v "$c" >/dev/null || die "$c not found"
done

if [[ -z $svc_user ]]; then
	unit=$(systemctl list-units 'mcsman@*.service' --state=active --no-legend --plain | awk '{print $1; exit}')
	svc_user=${unit#mcsman@}
	svc_user=${svc_user%.service}
	[[ -n $svc_user ]] || svc_user=$(id -un)
fi
unit="mcsman@$svc_user"
db=/var/lib/mcsman/mcsman.db

confirm() {
	((yes)) && return
	read -rp "$1 [y/N] " a
	[[ $a == [yY]* ]] || die "aborted"
}

wait_ready() {
	for _ in $(seq 60); do
		mcsctl server list >/dev/null 2>&1 && return
		sleep 1
	done
	sudo journalctl -u "$unit" -n 30 --no-pager >&2 || true
	return 1
}

if [[ -n $rollback ]]; then
	pkg=$(ls "$rollback"/mcsman-*.pkg.tar.zst 2>/dev/null | head -1) || true
	[[ -f $rollback/mcsman.db ]] || die "$rollback/mcsman.db not found"
	confirm "Stop $unit and restore ${pkg:-the database only} from $rollback?"
	sudo systemctl stop "$unit"
	[[ -n $pkg ]] && sudo pacman -U --noconfirm "$pkg"
	sudo rm -f "$db-wal" "$db-shm"
	sudo cp -a "$rollback"/mcsman.db* /var/lib/mcsman/
	sudo systemctl start "$unit"
	wait_ready || die "$unit did not come back; see the log above"
	say "Rolled back. Start any servers you need with mcsctl server start."
	exit 0
fi

cd "$(dirname "$(realpath "$0")")"
systemctl is-active --quiet "$unit" || die "$unit is not running (use -u USER)"
mcsctl server list >/dev/null || die "can't reach the daemon with mcsctl (are you in the service user's group?)"

if ((pull)); then
	say "Pulling"
	git -C ../.. pull --ff-only
fi

# Save the previous package before makepkg -f overwrites it.
snap=~/mcsman-pre-update/$(date +%Y%m%d-%H%M%S)
mkdir -p "$snap"
old_pkg=$(makepkg --packagelist | head -1)
if [[ -f $old_pkg ]]; then
	cp -a "$old_pkg" "$snap/"
else
	echo "warning: no previous package file to keep; a rollback can only restore the database" >&2
fi

# Build while the servers keep running.
say "Building"
makepkg -sf
new_pkg=$(makepkg --packagelist | head -1)
[[ -f $new_pkg ]] || die "build produced no package"

# The freshly built mcsctl reports autostart (older ones don't).
list=$(src/mcsman/bin/mcsctl --json server list)
up='select(.status == "running" or .status == "starting")'
mapfile -t running < <(jq -r ".[] | $up | .name" <<<"$list")
mapfile -t manual < <(jq -r ".[] | $up | select(.autostart | not) | .name" <<<"$list")

echo
echo "Ready to install $(basename "$new_pkg") for $unit."
if ((${#running[@]})); then
	echo "Running servers will be stopped with a ${delay}s countdown: ${running[*]}"
fi
confirm "Continue?"

if ((${#running[@]})); then
	say "Stopping servers"
	pids=()
	for s in "${running[@]}"; do
		mcsctl server stop "$s" --delay "$delay" &
		pids+=($!)
	done
	for p in "${pids[@]}"; do wait "$p"; done
	if ((backup)); then
		for s in "${running[@]}"; do mcsctl backup create "$s"; done
	fi
fi

rollback_hint() {
	echo "Snapshot: $snap" >&2
	echo "To roll back: $0 --rollback $snap" >&2
}
trap 'echo "update.sh: failed" >&2; rollback_hint' ERR

say "Stopping $unit"
sudo systemctl stop "$unit"
# As root: the state directory may not be readable by you.
sudo find /var/lib/mcsman -maxdepth 1 -name 'mcsman.db*' -exec cp -a {} "$snap/" +
sudo cp -a /etc/mcsman "$snap/"

say "Installing"
sudo pacman -U --noconfirm "$new_pkg"

say "Starting $unit"
sudo systemctl start "$unit"
wait_ready

for s in "${manual[@]}"; do
	mcsctl server start "$s" || echo "warning: could not start $s" >&2
done
trap - ERR

if compgen -G '/etc/mcsman/*.pacnew' >/dev/null; then
	echo "Note: new default config in /etc/mcsman/*.pacnew; merge with sudo pacdiff" >&2
fi
say "Updated. Autostart servers are coming up on their own."
rollback_hint 2>&1

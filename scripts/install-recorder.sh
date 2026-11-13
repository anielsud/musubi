#!/bin/sh
# Installs bin/Musubi Recorder.app (from `make recorder-app`) into
# ~/Applications and (re)loads its LaunchAgent, so the recorder runs at
# login beside Tomoe. Reloading stops a running recorder first; it saves
# a recording in progress, but the rest of that meeting isn't recorded,
# so it refuses while a meeting is being recorded (see below).
#
# Usage: scripts/install-recorder.sh [data folder]
# (default ~/.local/share/musubi/recorder; never Tomoe's folder)
set -eu

label=com.anielsud.musubi.recorder
repo=$(cd "$(dirname "$0")/.." && pwd)
app_src="$repo/bin/Musubi Recorder.app"
app="$HOME/Applications/Musubi Recorder.app"
exe="$app/Contents/MacOS/musubi-recorder"
data=${1:-${XDG_DATA_HOME:-$HOME/.local/share}/musubi/recorder}
agent="$HOME/Library/LaunchAgents/$label.plist"

if [ ! -d "$app_src" ]; then
	echo "No $app_src: run make recorder-app first." >&2
	exit 1
fi
case "$data" in
*'|'* | *'&'*) echo "Data folder may not contain | or &: $data" >&2; exit 1 ;;
esac

domain="gui/$(id -u)"

# Refuse during a meeting: stopping the recorder saves what it has, but
# the rest of that meeting is lost (2026-11-12, 11:31 call cut short).
# The new build asks the running recorder; one too old to answer counts
# as idle. MUSUBI_RECORDER_FORCE_INSTALL=1 installs anyway.
if [ "${MUSUBI_RECORDER_FORCE_INSTALL:-}" != 1 ]; then
	set +e
	state=$("$app_src/Contents/MacOS/musubi-recorder" status --data "$data" 2>&1)
	rc=$?
	set -e
	case $rc in
	0) ;;
	3)
		echo "The recorder is $state; not installing. Run this after the meeting (or set MUSUBI_RECORDER_FORCE_INSTALL=1)." >&2
		exit 1
		;;
	*)
		echo "Couldn't tell whether the recorder is recording ($state); not installing. Try again, or set MUSUBI_RECORDER_FORCE_INSTALL=1." >&2
		exit 1
		;;
	esac
fi

# Stop the running one before its files are replaced, and wait until it
# has gone: it saves a recording in progress first, and loading the new
# one while launchd still holds the old fails ("Bootstrap failed: 5"),
# which left no recorder running at all.
launchctl bootout "$domain/$label" 2>/dev/null || true
i=0
while launchctl print "$domain/$label" >/dev/null 2>&1; do
	i=$((i + 1))
	if [ "$i" -gt 180 ]; then
		echo "The old recorder hasn't stopped after 3 minutes; not installing." >&2
		exit 1
	fi
	sleep 1
done

mkdir -p "$HOME/Applications" "$data/logs"
rm -rf "$app"
ditto "$app_src" "$app"

mkdir -p "$(dirname "$agent")"
sed -e "s|__EXECUTABLE__|$exe|g" -e "s|__DATA__|$data|g" \
	"$repo/packaging/macos/recorder/launchagent.plist.template" > "$agent"
plutil -lint "$agent" >/dev/null

# The recorder checks the data folder itself and exits if it is Tomoe's.
n=0
until launchctl bootstrap "$domain" "$agent" 2>/dev/null; do
	n=$((n + 1))
	if [ "$n" -ge 10 ]; then
		echo "Couldn't load $label: the recorder is NOT running." >&2
		exit 1
	fi
	sleep 3
done

# Loaded is not running: check it came up.
n=0
until launchctl print "$domain/$label" 2>/dev/null | grep -q "state = running"; do
	n=$((n + 1))
	if [ "$n" -ge 30 ]; then
		echo "$label is loaded but not running: the recorder is NOT recording. See $data/logs/recorder.log" >&2
		exit 1
	fi
	sleep 1
done
echo "Installed $app; $label running. Log: $data/logs/recorder.log"

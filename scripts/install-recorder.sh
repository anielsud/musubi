#!/bin/sh
# Installs bin/Musubi Recorder.app (from `make recorder-app`) into
# ~/Applications and (re)loads its LaunchAgent, so the recorder runs at
# login beside Tomoe. Reloading stops a running recorder first; it saves
# a recording in progress, but the rest of that meeting isn't recorded,
# so don't run this during a meeting.
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

# Stop the running one before its files are replaced.
launchctl bootout "gui/$(id -u)/$label" 2>/dev/null || true

mkdir -p "$HOME/Applications" "$data/logs"
rm -rf "$app"
ditto "$app_src" "$app"

mkdir -p "$(dirname "$agent")"
sed -e "s|__EXECUTABLE__|$exe|g" -e "s|__DATA__|$data|g" \
	"$repo/packaging/macos/recorder/launchagent.plist.template" > "$agent"
plutil -lint "$agent" >/dev/null

# The recorder checks the data folder itself and exits if it is Tomoe's.
launchctl bootstrap "gui/$(id -u)" "$agent"
echo "Installed $app; agent $label loaded. Log: $data/logs/recorder.log"

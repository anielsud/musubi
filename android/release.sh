#!/bin/bash
# Builds the signed release APK of the Musubi phone app (docs/phone.md, "Updates").
#
#   android/release.sh            build, sign, write dist/musubi-phone-<v>.apk and .apk.sha256
#   android/release.sh --publish  the same, then publish them as GitHub release android-v<v>
#
# The release key lives outside git at ~/.config/musubi/android/release.jks (mode 0600); it is
# made on the first run. Its password is random and kept only in the macOS Keychain (service
# musubi-android-release); it reaches Gradle through the environment of one --no-daemon build.
set -euo pipefail

PUBLISH=0
case "${1:-}" in
  "") ;;
  --publish) PUBLISH=1 ;;
  *) echo "usage: $0 [--publish]" >&2; exit 2 ;;
esac

HERE="$(cd "$(dirname "$0")" && pwd)"
KEY_DIR="$HOME/.config/musubi/android"
KEYSTORE="$KEY_DIR/release.jks"
ALIAS="musubi-phone"
SERVICE="musubi-android-release"
ACCOUNT="keystore-password"
REPO="anielsud/musubi-releases"

: "${JAVA_HOME:=/opt/homebrew/opt/openjdk@17}"
: "${ANDROID_HOME:=/opt/homebrew/share/android-commandlinetools}"
export JAVA_HOME ANDROID_HOME
KEYTOOL="$JAVA_HOME/bin/keytool"
APKSIGNER="$(ls -d "$ANDROID_HOME"/build-tools/*/ | sort -V | tail -1)apksigner"

keychain_has() { security find-generic-password -s "$SERVICE" -a "$ACCOUNT" >/dev/null 2>&1; }

# In CI (.github/workflows/android-release.yml) the key comes from repository secrets: the
# workflow writes the keystore to a 0600 temp file and passes its path and password here.
if [[ -n "${MUSUBI_RELEASE_KEYSTORE:-}" ]]; then
  KEYSTORE="$MUSUBI_RELEASE_KEYSTORE"
  [[ -n "${MUSUBI_RELEASE_PASSWORD:-}" ]] || { echo "MUSUBI_RELEASE_PASSWORD is not set." >&2; exit 1; }
fi

if [[ -z "${MUSUBI_RELEASE_PASSWORD:-}" && ! -f "$KEYSTORE" ]]; then
  if keychain_has; then
    echo "The Keychain has a $SERVICE password but $KEYSTORE is missing. Restore the keystore; not making a new key." >&2
    exit 1
  fi
  echo "Creating the release key at $KEYSTORE (first run)."
  umask 077
  mkdir -p "$KEY_DIR"
  chmod 700 "$KEY_DIR"
  MUSUBI_KS_PASS="$(openssl rand -base64 48 | tr -dc 'A-Za-z0-9' | head -c 40)"
  [[ ${#MUSUBI_KS_PASS} -ge 32 ]] || { echo "Could not generate a password." >&2; exit 1; }
  export MUSUBI_KS_PASS
  # PKCS12 uses one password for the store and the key. Passed via the environment, never argv.
  "$KEYTOOL" -genkeypair -keystore "$KEYSTORE" -storetype PKCS12 -alias "$ALIAS" \
    -keyalg RSA -keysize 4096 -sigalg SHA256withRSA -validity 10958 \
    -dname "CN=Musubi phone" -storepass:env MUSUBI_KS_PASS -keypass:env MUSUBI_KS_PASS >/dev/null
  chmod 600 "$KEYSTORE"
  # `security -i` reads the command from stdin, so the password is not on any command line.
  printf 'add-generic-password -s %s -a %s -l "Musubi Android release key" -w "%s"\n' "$SERVICE" "$ACCOUNT" "$MUSUBI_KS_PASS" | security -i >/dev/null
  keychain_has || { echo "Could not store the password in the Keychain." >&2; exit 1; }
  unset MUSUBI_KS_PASS
fi

[[ "$(stat -f %Lp "$KEYSTORE")" == "600" ]] || { echo "$KEYSTORE must be mode 0600." >&2; exit 1; }
if [[ -n "${MUSUBI_RELEASE_PASSWORD:-}" ]]; then
  PASS="$MUSUBI_RELEASE_PASSWORD"
else
  PASS="$(security find-generic-password -s "$SERVICE" -a "$ACCOUNT" -w 2>/dev/null)" || {
    echo "No $SERVICE password in the Keychain." >&2; exit 1; }
fi

VERSION="$(sed -n 's/^ *versionName = "\([0-9][0-9.]*\)"$/\1/p' "$HERE/app/build.gradle.kts")"
[[ "$VERSION" =~ ^[0-9]+(\.[0-9]+){1,3}$ ]] || { echo "Could not read versionName." >&2; exit 1; }
TAG="android-v$VERSION"
NAME="musubi-phone-$VERSION.apk"

cd "$HERE"
# Empty mock properties override anything in gradle.properties: real builds talk only to GitHub.
MUSUBI_RELEASE_KEYSTORE="$KEYSTORE" MUSUBI_RELEASE_KEY_ALIAS="$ALIAS" \
MUSUBI_RELEASE_STORE_PASSWORD="$PASS" MUSUBI_RELEASE_KEY_PASSWORD="$PASS" \
  ./gradlew --no-daemon -q clean assembleRelease -PupdatesMockBase= -PupdatesMockPin=
unset PASS

OUT="app/build/outputs/apk/release/app-release.apk"
[[ -f "$OUT" ]] || { echo "No signed release APK at $OUT." >&2; exit 1; }
"$APKSIGNER" verify "$OUT" >/dev/null

mkdir -p dist
cp "$OUT" "dist/$NAME"
(cd dist && shasum -a 256 "$NAME" > "$NAME.sha256")
echo "Built dist/$NAME"
echo "SHA-256 $(cut -d' ' -f1 "dist/$NAME.sha256")"
"$APKSIGNER" verify --print-certs "dist/$NAME" | grep -E 'certificate (DN|SHA-256)'

if [[ $PUBLISH -eq 1 ]]; then
  gh release create "$TAG" --repo "$REPO" --title "Musubi phone $VERSION" --notes "Android app $VERSION." \
    "dist/$NAME" "dist/$NAME.sha256"
  echo "Published $TAG to $REPO."
fi

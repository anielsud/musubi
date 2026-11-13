#!/usr/bin/env bash
# Builds the live-text engine (sherpa-onnx's JNI library, arm64-v8a) from pinned, hash-checked
# source into android/sherpa-onnx/out/ (gitignored). Gradle runs it before every build; it does
# nothing when out/ already holds this exact version.
#
# Why from source: the prebuilt sherpa-onnx AARs are built with TTS on, which statically links
# espeak-ng (GPL-3.0). This build turns TTS and speaker diarization off, so only permissively
# licensed code goes in: sherpa-onnx (Apache-2.0), onnxruntime (MIT), kaldi-native-fbank,
# kaldi-decoder, simple-sentencepiece, openfst (Apache-2.0), nlohmann/json (MIT) and Eigen
# headers (MPL-2.0). Every dependency is fetched by sherpa-onnx's CMake with a pinned SHA-256.
#
# Needs the Android NDK named below (sdkmanager "ndk;27.0.11718014") and SDK CMake 3.31.5
# (sdkmanager "cmake;3.31.5"), found through ANDROID_SDK_ROOT or ANDROID_HOME.
set -euo pipefail

VERSION=1.13.8
SRC_URL="https://github.com/k2-fsa/sherpa-onnx/archive/refs/tags/v${VERSION}.tar.gz"
SRC_SHA256=b0374cc56dbc186d442ae73d5de743bb092470b640c4c50ce7b029044c0c4fa8
NDK_VERSION=27.0.11718014
CMAKE_VERSION=3.31.5
ABI=arm64-v8a
# The build is reproducible (paths are mapped away), so its output is pinned too.
LIB_SHA256=f02a737609ee1b13de59c04bd9d0a9b2401f03c41d1338c92595e8f5d0c2c758
STAMP="sherpa-onnx ${VERSION} ${SRC_SHA256} ndk ${NDK_VERSION} ${ABI} tts=off diarization=off"

here="$(cd "$(dirname "$0")" && pwd)"
out="$here/out"
work="$here/.work"

sha256() { shasum -a 256 "$1" | awk '{print $1}'; }

if [ -f "$out/STAMP" ] && [ "$(cat "$out/STAMP")" = "$STAMP" ] && [ -f "$out/jniLibs/$ABI/libsherpa-onnx-jni.so" ] &&
  [ "$(sha256 "$out/jniLibs/$ABI/libsherpa-onnx-jni.so")" = "$LIB_SHA256" ]; then
  exit 0
fi

sdk="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}"
if [ -z "$sdk" ]; then
  echo "sherpa-onnx: set ANDROID_SDK_ROOT (or ANDROID_HOME)" >&2
  exit 1
fi
ndk="$sdk/ndk/$NDK_VERSION"
cmake="$sdk/cmake/$CMAKE_VERSION/bin/cmake"
ninja="$sdk/cmake/$CMAKE_VERSION/bin/ninja"
if [ ! -f "$ndk/build/cmake/android.toolchain.cmake" ]; then
  echo "sherpa-onnx: NDK $NDK_VERSION missing; run: sdkmanager \"ndk;$NDK_VERSION\"" >&2
  exit 1
fi
if [ ! -x "$cmake" ] || [ ! -x "$ninja" ]; then
  echo "sherpa-onnx: CMake $CMAKE_VERSION missing; run: sdkmanager \"cmake;$CMAKE_VERSION\"" >&2
  exit 1
fi

mkdir -p "$work"
tarball="$work/sherpa-onnx-$VERSION.tar.gz"
if [ ! -f "$tarball" ] || [ "$(sha256 "$tarball")" != "$SRC_SHA256" ]; then
  rm -f "$tarball.part"
  curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error --max-filesize 100000000 \
    -o "$tarball.part" "$SRC_URL"
  got="$(sha256 "$tarball.part")"
  if [ "$got" != "$SRC_SHA256" ]; then
    rm -f "$tarball.part"
    echo "sherpa-onnx: source SHA-256 mismatch (got $got)" >&2
    exit 1
  fi
  mv "$tarball.part" "$tarball"
fi

src="$work/sherpa-onnx-$VERSION"
rm -rf "$src" "$work/build"
tar -xzf "$tarball" -C "$work"
build="$work/build"
mkdir -p "$build"

# Paths are mapped away so the library does not embed this machine's folders.
prefix_map="-ffile-prefix-map=$src=/sherpa-onnx -ffile-prefix-map=$build=/build"

"$cmake" -S "$src" -B "$build" -G Ninja -DCMAKE_MAKE_PROGRAM="$ninja" \
  -DCMAKE_TOOLCHAIN_FILE="$ndk/build/cmake/android.toolchain.cmake" \
  -DANDROID_ABI="$ABI" -DANDROID_PLATFORM=android-26 \
  -DCMAKE_BUILD_TYPE=Release \
  -DCMAKE_C_FLAGS="$prefix_map" -DCMAKE_CXX_FLAGS="$prefix_map" \
  -DBUILD_SHARED_LIBS=OFF \
  -DSHERPA_ONNX_ENABLE_TTS=OFF \
  -DSHERPA_ONNX_ENABLE_SPEAKER_DIARIZATION=OFF \
  -DSHERPA_ONNX_ENABLE_JNI=ON \
  -DSHERPA_ONNX_ENABLE_C_API=OFF \
  -DSHERPA_ONNX_ENABLE_BINARY=OFF \
  -DSHERPA_ONNX_ENABLE_PYTHON=OFF \
  -DSHERPA_ONNX_ENABLE_TESTS=OFF \
  -DSHERPA_ONNX_ENABLE_CHECK=OFF \
  -DSHERPA_ONNX_ENABLE_PORTAUDIO=OFF \
  -DSHERPA_ONNX_ENABLE_WEBSOCKET=OFF \
  -DSHERPA_ONNX_ENABLE_RKNN=OFF \
  -DSHERPA_ONNX_ENABLE_QNN=OFF \
  -DSHERPA_ONNX_LINK_LIBSTDCPP_STATICALLY=OFF
"$cmake" --build "$build" --target sherpa-onnx-jni

lib="$(find "$build" -name libsherpa-onnx-jni.so -type f | head -1)"
[ -n "$lib" ] || { echo "sherpa-onnx: build produced no library" >&2; exit 1; }

# Guard: nothing from espeak-ng / piper (GPL) may be in the library.
if strings -a "$lib" | grep -qi -E 'espeak|piper'; then
  echo "sherpa-onnx: library contains espeak/piper code; refusing" >&2
  exit 1
fi

rm -rf "$out"
mkdir -p "$out/jniLibs/$ABI" "$out/kotlin/com/k2fsa/sherpa/onnx"
"$ndk"/toolchains/llvm/prebuilt/*/bin/llvm-strip --strip-unneeded -o "$out/jniLibs/$ABI/libsherpa-onnx-jni.so" "$lib"
# Only the Kotlin wrappers the online recognizer needs (Apache-2.0, unchanged).
for f in OnlineRecognizer.kt OnlineStream.kt FeatureConfig.kt HomophoneReplacerConfig.kt QnnConfig.kt; do
  cp "$src/sherpa-onnx/kotlin-api/$f" "$out/kotlin/com/k2fsa/sherpa/onnx/$f"
done
cp "$src/LICENSE" "$out/LICENSE-sherpa-onnx"
got="$(sha256 "$out/jniLibs/$ABI/libsherpa-onnx-jni.so")"
if [ "$got" != "$LIB_SHA256" ]; then
  rm -rf "$out"
  echo "sherpa-onnx: built library SHA-256 $got does not match the pinned $LIB_SHA256" >&2
  exit 1
fi
echo "$STAMP" > "$out/STAMP"
rm -rf "$src" "$build"
echo "sherpa-onnx: built $VERSION ($ABI)"

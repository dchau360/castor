#!/usr/bin/env bash
# Builds castor.apk from this directory with the bare Android SDK (no Gradle): aapt2, javac, d8, zipalign, apksigner.
# Needs ANDROID_HOME with platforms/android-34 and a build-tools release, and a JDK 17+ on PATH or JAVA_HOME.
# Bump VERSION whenever the app changes: castor reinstalls the app on any device holding another version.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
sdk="${ANDROID_HOME:-${ANDROID_SDK_ROOT:?set ANDROID_HOME}}"
platform="$sdk/platforms/android-34/android.jar"
tools="$(ls -d "$sdk"/build-tools/* | sort -V | tail -1)"
version="$(tr -d '[:space:]' < "$here/VERSION")"
javac="${JAVA_HOME:+$JAVA_HOME/bin/}javac"
keytool="${JAVA_HOME:+$JAVA_HOME/bin/}keytool"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

"$tools/aapt2" link -o "$work/unsigned.apk" -I "$platform" \
  --manifest "$here/AndroidManifest.xml" \
  --min-sdk-version 25 --target-sdk-version 34 \
  --version-code "$version" --version-name "$version"

mkdir -p "$work/classes" "$work/dex"
"$javac" --release 11 -classpath "$platform" -d "$work/classes" $(find "$here/src" -name '*.java')
"$tools/d8" --release --min-api 25 --lib "$platform" --output "$work/dex" $(find "$work/classes" -name '*.class')
(cd "$work/dex" && zip -q "$work/unsigned.apk" classes.dex)

"$tools/zipalign" -f 4 "$work/unsigned.apk" "$work/aligned.apk"

# Any key will do: castor replaces an install signed by another one.
keystore="${CASTOR_APK_KEYSTORE:-$work/debug.keystore}"
if [[ ! -f "$keystore" ]]; then
  "$keytool" -genkeypair -keystore "$keystore" -storepass android -keypass android -alias castor \
    -keyalg RSA -keysize 2048 -validity 10000 -dname "CN=castor" >/dev/null 2>&1
fi
"$tools/apksigner" sign --ks "$keystore" --ks-pass pass:android --key-pass pass:android \
  --out "$here/castor.apk" "$work/aligned.apk"
rm -f "$here/castor.apk.idsig"
echo "built $here/castor.apk (version $version)"

#!/usr/bin/env bash
set -euo pipefail

apk=${1:?Usage: smoke-release-android.sh path/to/release.apk}
adb_binary=${MULTICA_ADB:-adb}
package=ai.multica.mobile.local

show_failure_logs() {
  result=$?
  if [ "$result" -ne 0 ]; then
    "$adb_binary" logcat -d -s AndroidRuntime:E ReactNativeJS:E || true
  fi
  exit "$result"
}
trap show_failure_logs EXIT

"$adb_binary" wait-for-device
"$adb_binary" install -r "$apk"
"$adb_binary" shell am force-stop "$package"
"$adb_binary" logcat -c
launch_output=$("$adb_binary" shell am start -W -n "$package/.MainActivity")
printf '%s\n' "$launch_output"
grep -q '^Status: ok' <<< "$launch_output"

# A successful ActivityManager launch can still immediately crash in JNI or JS.
for attempt in 1 2 3 4 5 6; do
  sleep 5
  pid=$("$adb_binary" shell pidof "$package" | tr -d '\r')
  test -n "$pid"
done

printf 'Verified signed Android release startup: %s stayed running for 30 seconds\n' "$package"

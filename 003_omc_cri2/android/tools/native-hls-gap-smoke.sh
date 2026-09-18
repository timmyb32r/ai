#!/usr/bin/env bash
set -euo pipefail

# Use ONLY a disposable emulator named CRI_Gap_Smoke. This installs test APKs there.
# Requires an existing Android SDK, ffmpeg, python3, and enough space to boot its AVD.
serial="${1:?Usage: native-hls-gap-smoke.sh emulator-PORT}"
case "$serial" in emulator-*) ;; *) echo 'A disposable emulator serial is required.' >&2; exit 1;; esac
project="$(cd "$(dirname "$0")/.." && pwd)"
sdk="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-$HOME/Library/Android/sdk}}"
adb="$sdk/platform-tools/adb"
ffmpeg="${FFMPEG:-ffmpeg}"
avd_name="$("$adb" -s "$serial" emu avd name | head -n 1 | tr -d '\r')"
test "$avd_name" = CRI_Gap_Smoke || { echo 'Refusing to install on a personal AVD; create CRI_Gap_Smoke first.' >&2; exit 1; }
fixture="$(mktemp -d "${TMPDIR:-/tmp}/cri-native-gap.XXXXXX")"
server_pid=''
port=''
cleanup() {
    if test -n "$port"; then "$adb" -s "$serial" reverse --remove "tcp:$port" >/dev/null 2>&1 || true; fi
    if test -n "$server_pid"; then kill "$server_pid" 2>/dev/null || true; wait "$server_pid" 2>/dev/null || true; fi
    rm -rf "$fixture"
}
trap cleanup EXIT
mkdir "$fixture/first" "$fixture/second"
for spec in first:440 second:880; do
    name="${spec%:*}"
    frequency="${spec#*:}"
    "$ffmpeg" -hide_banner -loglevel error -f lavfi -i "sine=frequency=$frequency:sample_rate=16000:duration=4" \
        -c:a libmp3lame -ac 1 -ar 16000 -b:a 64k -f hls -hls_time 3 -hls_list_size 0 \
        -hls_segment_filename "$fixture/$name/%09d.ts" "$fixture/$name/index.m3u8"
done
cp "$fixture/first/000000000.ts" "$fixture/000000001.ts"
cp "$fixture/second/000000000.ts" "$fixture/000000003.ts"
cat > "$fixture/stream.m3u8" <<'PLAYLIST'
#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:4
#EXT-X-MEDIA-SEQUENCE:1
#EXT-X-PLAYLIST-TYPE:VOD
#EXT-X-PROGRAM-DATE-TIME:2025-09-19T00:00:00.000Z
#EXTINF:3.024000,
000000001.ts
#EXT-X-DISCONTINUITY
#EXT-X-PROGRAM-DATE-TIME:2025-09-19T00:05:03.024Z
#EXTINF:3.024000,
000000003.ts
#EXT-X-ENDLIST
PLAYLIST
python3 - "$fixture" <<'PY' &
import functools, http.server, pathlib, sys
root = pathlib.Path(sys.argv[1])
handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=str(root))
server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
(root / "port").write_text(str(server.server_address[1]))
server.serve_forever()
PY
server_pid=$!
for _ in {1..100}; do test -f "$fixture/port" && break; sleep 0.1; done
port="$(cat "$fixture/port")"
"$adb" -s "$serial" reverse "tcp:$port" "tcp:$port"
cd "$project"
./gradlew :app:assembleDebug :app:assembleDebugAndroidTest \
    -PnativeSmokeRunner=com.crimobile.player.NativeHlsGapSmoke --offline --no-daemon
"$adb" -s "$serial" install -r app/build/outputs/apk/debug/app-debug.apk
"$adb" -s "$serial" install -r app/build/outputs/apk/androidTest/debug/app-debug-androidTest.apk
output="$("$adb" -s "$serial" shell am instrument -w -e url "http://127.0.0.1:$port/stream.m3u8" \
    com.crimobile.test/com.crimobile.player.NativeHlsGapSmoke)"
printf '%s\n' "$output"
case "$output" in *'Native HLS gap smoke passed:'*) ;; *) exit 1;; esac

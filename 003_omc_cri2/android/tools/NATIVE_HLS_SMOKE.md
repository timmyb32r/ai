# Native HLS gap smoke

`native-hls-gap-smoke.sh` exercises the real Media3 decoder and
`ExoRadioPlayer` on an Android emulator. It generates two independent MP3/TS
segments with ffmpeg, serves a playlist with a 300-second epoch gap and
`EXT-X-DISCONTINUITY`, and verifies:

- Both segments actually enter playing state and native decoding reports an audio format.
- The subtitle timeline follows each segment's actual program date time.
- The gap event occurs once, only when playback reaches the gap.
- Playback completes without waiting 300 seconds for the missing interval.
- Seeking into the missing interval lands on the next available segment.

Create and boot a **disposable** AVD named `CRI_Gap_Smoke` using an installed
Android image. The script refuses physical devices and differently named AVDs.
It installs the application and test APK into that emulator, so do not use a
device that contains personal application data. It removes its own temporary
HTTP fixture and ADB port forwarding when done; shut down/delete the disposable
AVD separately.

```sh
export JAVA_HOME=/path/to/jdk-21
export ANDROID_SDK_ROOT=/path/to/android-sdk
./tools/native-hls-gap-smoke.sh emulator-5588
```

Dependencies: the existing Gradle dependency cache, Android SDK, Python 3 and
ffmpeg on `PATH` (or `FFMPEG=/path/to/ffmpeg`). There are no SDK downloads in the
script. The instrumentation runner has no additional Maven dependencies.

Validation on 2026-09-18: the native smoke passed on a disposable Android 34
ARM64 emulator using Media3 1.4.1. Both TS segments were decoded with rendered
audio buffers, epoch mapping remained correct across the 300-second gap, the
notification occurred at the crossing, playback completed without an outage
wait, and seeking into the gap selected the next segment. Application and
instrumentation APK builds succeeded; all 142 JVM tests passed with zero failures,
errors or skips. The emulator and its temporary userdata
were removed after testing; no personal device or AVD was modified.

The initial attempt lacked disk space; after free space recovered, the fresh
emulator booted and the actual native test completed successfully. Provisioning
this installed Google Play image required about 7.37 GiB for userdata.

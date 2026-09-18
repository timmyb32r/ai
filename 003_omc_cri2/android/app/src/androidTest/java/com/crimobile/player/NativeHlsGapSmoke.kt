package com.crimobile.player

import android.app.Activity
import android.app.Instrumentation
import android.os.Bundle
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.datasource.DefaultHttpDataSource
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.hls.HlsMediaSource
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch

/** Opt-in native smoke runner; its HTTP fixture is supplied by tools/native-hls-gap-smoke.sh. */
class NativeHlsGapSmoke : Instrumentation() {
    private lateinit var args: Bundle

    override fun onCreate(arguments: Bundle?) {
        super.onCreate(arguments)
        args = arguments ?: Bundle()
        start()
    }

    override fun onStart() {
        val result = Bundle()
        try {
            smoke()
            result.putString("stream", "Native HLS gap smoke passed: decoded both segments, mapped epochs, notified at crossing, completed without waiting through outage.\n")
            finish(Activity.RESULT_OK, result)
        } catch (e: Throwable) {
            result.putString("stream", "Native HLS gap smoke FAILED: ${e.stackTraceToString()}\n")
            finish(Activity.RESULT_CANCELED, result)
        }
    }

    private fun smoke() {
        val url = requireNotNull(args.getString("url")) { "url argument required" }
        val firstEpoch = 1_758_240_000_000L
        val secondEpoch = firstEpoch + 303_024L
        val firstDuration = 3_024L
        val scope = CoroutineScope(Dispatchers.Main)
        lateinit var exo: ExoPlayer
        lateinit var radio: ExoRadioPlayer
        var release: (() -> Unit)? = null
        val gaps = mutableListOf<Pair<Long, Long>>()
        val failures = mutableListOf<String>()
        var firstPlayed = false
        var secondPlayed = false
        var ended = false
        var samples = 0
        try {
            runOnMainSync {
                val maps = TimelinePlaylistParserFactory()
                exo = ExoPlayer.Builder(targetContext).setMediaSourceFactory(
                    HlsMediaSource.Factory(DefaultHttpDataSource.Factory())
                        .setPlaylistParserFactory(maps)
                        .setTimestampAdjusterInitializationTimeoutMs(10_000)
                ).build()
                release = { exo.release() }
                exo.addListener(object : Player.Listener {
                    override fun onPlayerError(error: PlaybackException) { failures += error.toString() }
                })
                radio = ExoRadioPlayer(exo, maps)
                release = { radio.release() }
                scope.launch { radio.gapEvents.collect { gaps += exo.currentPosition to it } }
                radio.play(url)
            }
            val deadline = System.nanoTime() + 25_000_000_000L
            while (!ended && System.nanoTime() < deadline) {
                Thread.sleep(50)
                runOnMainSync {
                    check(failures.isEmpty()) { "Native player errors: $failures" }
                    val position = exo.currentPosition
                    if (exo.isPlaying) {
                        samples++
                        val actual = radio.currentTimelineMs.value
                        // The adapter refreshes every 100ms; allow one refresh at a boundary.
                        if (position in 300L..(firstDuration - 150L)) {
                            firstPlayed = true
                            check(kotlin.math.abs(actual - (firstEpoch + position)) < 200) {
                                "First segment epoch drift at $position: $actual"
                            }
                            check(gaps.isEmpty()) { "Gap reported before audio reached it: $gaps" }
                        }
                        if (position > firstDuration + 200L) {
                            secondPlayed = true
                            check(kotlin.math.abs(actual - (secondEpoch + position - firstDuration)) < 200) {
                                "Second segment epoch drift at $position: $actual"
                            }
                        }
                    }
                    ended = exo.playbackState == Player.STATE_ENDED
                }
            }
            runOnMainSync {
                check(ended) { "Playback did not finish within 25s" }
                check(firstPlayed && secondPlayed) { "Did not play both segments: $firstPlayed/$secondPlayed ($samples samples)" }
                check(exo.audioFormat != null) { "Native audio format was not decoded" }
                val rendered = exo.audioDecoderCounters?.renderedOutputBufferCount ?: 0
                check(rendered > 0) { "Native decoder did not render any audio buffers" }
                check(gaps.size == 1 && gaps.single().first >= firstDuration && gaps.single().second == 300_000L) {
                    "Expected one 300s gap at crossing; observed $gaps"
                }
                radio.seekTo(firstEpoch + 120_000L)
                check(kotlin.math.abs(exo.currentPosition - firstDuration) < 2) { "Seeking into outage failed: ${exo.currentPosition}" }
            }
        } finally {
            runOnMainSync {
                scope.cancel()
                release?.invoke()
            }
        }
    }
}

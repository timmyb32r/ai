package com.crimobile.pronounce

import com.crimobile.model.WordEntry
import com.crimobile.player.RadioPlayer
import kotlinx.coroutines.*
import com.crimobile.debug.DebugLogger

private const val TAG = "CRIRadio:pronounce"

/**
 * Plays the audio segment corresponding to a word by seeking the active radio
 * player to the word's epoch timestamp.
 *
 * [playerProvider] is invoked on every [playWord] call to resolve the currently
 * active player (live HLS or offline .ts). This ensures pronounce works
 * correctly regardless of [PlaybackMode].
 */
class PronunciationPlayer(
    private val playerProvider: () -> RadioPlayer?,
    private val scope: CoroutineScope = CoroutineScope(Dispatchers.Main)
) {
    private var originalTimelineMs: Long = 0
    private var pronounceJob: Job? = null

    fun playWord(word: WordEntry, prevTimeTo: Double? = null, nextTimeFrom: Double? = null) {
        pronounceJob?.cancel()

        val player = playerProvider() ?: run {
            DebugLogger.w(TAG, "pronounce skipped — no active player")
            return
        }

        // Save current position
        originalTimelineMs = player.currentTimelineMs.value

        val startSec = if (prevTimeTo != null) (prevTimeTo + word.start_sec) / 2.0 else word.start_sec
        val endSec = if (nextTimeFrom != null) (word.end_sec + nextTimeFrom) / 2.0 else word.end_sec

        val wordStartMs = (startSec * 1000).toLong()
        val wordDurationMs = ((endSec - startSec) * 1000).toLong().coerceAtLeast(200)

        DebugLogger.i(TAG, "pronounce word=${word.text} startMs=$wordStartMs durationMs=$wordDurationMs savedPosMs=$originalTimelineMs")

        player.pause()
        player.seekTo(wordStartMs)
        player.resume()

        // Auto-stop after word duration, then restore position
        pronounceJob = scope.launch {
            delay(wordDurationMs)
            DebugLogger.i(TAG, "pronounce done — restoring posMs=$originalTimelineMs")
            player.pause()
            player.seekTo(originalTimelineMs)
        }
    }

    fun stop() {
        pronounceJob?.cancel()
        pronounceJob = null
        val player = playerProvider() ?: return
        player.pause()
        if (originalTimelineMs > 0) {
            player.seekTo(originalTimelineMs)
        }
    }
}

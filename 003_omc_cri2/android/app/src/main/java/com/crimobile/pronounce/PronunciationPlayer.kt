package com.crimobile.pronounce

import android.util.Log
import com.crimobile.model.WordEntry
import com.crimobile.player.RadioPlayer
import kotlinx.coroutines.*

private const val TAG = "CRIRadio:pronounce"

class PronunciationPlayer(
    private val player: RadioPlayer,
    private val scope: CoroutineScope = CoroutineScope(Dispatchers.Main)
) {
    private var originalTimelineMs: Long = 0
    private var pronounceJob: Job? = null

    fun playWord(word: WordEntry, prevTimeTo: Double? = null, nextTimeFrom: Double? = null) {
        pronounceJob?.cancel()

        // Save current position
        originalTimelineMs = player.currentTimelineMs.value

        val startSec = if (prevTimeTo != null) (prevTimeTo + word.start_sec) / 2.0 else word.start_sec
        val endSec = if (nextTimeFrom != null) (word.end_sec + nextTimeFrom) / 2.0 else word.end_sec

        val wordStartMs = (startSec * 1000).toLong()
        val wordDurationMs = ((endSec - startSec) * 1000).toLong().coerceAtLeast(200)

        Log.i(TAG, "pronounce word=${word.text} startMs=$wordStartMs durationMs=$wordDurationMs savedPosMs=$originalTimelineMs")

        player.pause()
        player.seekTo(wordStartMs)
        player.resume()

        // Auto-stop after word duration, then restore position
        pronounceJob = scope.launch {
            delay(wordDurationMs)
            Log.i(TAG, "pronounce done — restoring posMs=$originalTimelineMs")
            player.pause()
            player.seekTo(originalTimelineMs)
        }
    }

    fun stop() {
        pronounceJob?.cancel()
        pronounceJob = null
        player.pause()
        if (originalTimelineMs > 0) {
            player.seekTo(originalTimelineMs)
        }
    }
}

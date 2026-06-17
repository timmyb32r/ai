package com.crimobile.service

import android.util.Log
import com.crimobile.api.HighlightTracker
import com.crimobile.model.SubtitleEvent
import com.crimobile.model.WordBoundary

/**
 * Pure-math [HighlightTracker] — no Android dependencies, no I/O, no mutable state.
 */
class HighlightTrackerImpl : HighlightTracker {

    override fun update(subtitle: SubtitleEvent, playCursor: Double): IntRange? {
        val textLen = subtitle.textZh.length
        if (textLen == 0) return null
        // Guard: playCursor must be within the subtitle's span.
        if (playCursor < subtitle.start || playCursor > subtitle.end) return null

        val elapsed = playCursor - subtitle.start

        return HighlightTracker.findWordByTime(elapsed, subtitle.words)
            ?: HighlightTracker.findWordRange(
                relAudioSec = playCursor,
                relStart = subtitle.start,
                relEnd = subtitle.end,
                textLength = textLen,
                words = subtitle.words,
            )
    }

    companion object {
        @Suppress("unused")
        private const val TAG = "HighlightTracker"
    }
}

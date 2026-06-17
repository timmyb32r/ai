package com.crimobile.api

import com.crimobile.model.SubtitleEvent
import com.crimobile.model.WordBoundary

/**
 * Pure-math utility for mapping audio playback position to the highlighted
 * character range within a subtitle.
 *
 * No Android dependencies, no I/O, no mutable state — fully testable on JVM.
 */
interface HighlightTracker {
    /**
     * Compute the highlighted character range for the given [playCursor]
     * (absolute timeline seconds) within [subtitle].
     *
     * Returns null if [playCursor] is outside [subtitle.start, subtitle.end].
     */
    fun update(subtitle: SubtitleEvent, playCursor: Double): IntRange?

    companion object {
        /**
         * Character-level fallback: maps a relative audio position to a
         * character index within the text, assuming uniform speech rate.
         */
        fun calculateHighlightIndex(
            relAudioSec: Double,
            relStart: Double,
            relEnd: Double,
            textLength: Int,
        ): Int {
            if (textLength <= 0) return -1
            val duration = relEnd - relStart
            if (duration <= 0.0) return -1
            if (relAudioSec < relStart || relAudioSec > relEnd) return -1
            val relPos = (relAudioSec - relStart).coerceIn(0.0, duration)
            return (relPos / duration * textLength).toInt().coerceIn(0, textLength - 1)
        }

        /**
         * Finds the word whose per-token timestamps contain [elapsedSec].
         * Returns null if no word has valid timestamps.
         */
        fun findWordByTime(
            elapsedSec: Double,
            words: List<WordBoundary>?,
        ): IntRange? {
            if (words.isNullOrEmpty()) return null
            val hasTimestamps = words.any { it.endSec > it.startSec }
            if (!hasTimestamps) return null
            val word = words.firstOrNull {
                elapsedSec >= it.startSec && elapsedSec < it.endSec
            } ?: return null
            return IntRange(word.charStart, word.charEnd - 1)
        }

        /**
         * Character-proportional fallback: maps a relative audio position
         * to a character range using word boundaries when available.
         */
        fun findWordRange(
            relAudioSec: Double,
            relStart: Double,
            relEnd: Double,
            textLength: Int,
            words: List<WordBoundary>?,
        ): IntRange? {
            if (textLength <= 0) return null
            val duration = relEnd - relStart
            if (duration <= 0.0) return null
            if (relAudioSec < relStart || relAudioSec > relEnd) return null
            val relPos = (relAudioSec - relStart).coerceIn(0.0, duration)
            val charIndex = (relPos / duration * textLength).toInt().coerceIn(0, textLength - 1)
            if (words.isNullOrEmpty()) return IntRange(charIndex, charIndex)
            val word = words.firstOrNull { charIndex in it.charStart until it.charEnd }
            return if (word != null) IntRange(word.charStart, word.charEnd - 1)
            else IntRange(charIndex, charIndex)
        }
    }
}

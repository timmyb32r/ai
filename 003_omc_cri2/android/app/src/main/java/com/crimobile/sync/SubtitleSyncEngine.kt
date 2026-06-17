package com.crimobile.sync

import com.crimobile.model.SubtitleSegment
import com.crimobile.model.WordEntry

/**
 * Maps playback timeline position (Unix epoch milliseconds) to the active
 * subtitle segment and word. Uses binary search for O(log n) lookup.
 *
 * Timeline correlation: both audio (via windowStartTimeMs from HLS
 * EXT-X-PROGRAM-DATE-TIME) and metadata (via server-side Unix epoch
 * timestamps) share the SAME system clock in the Docker container —
 * a single source of truth.
 */
class SubtitleSyncEngine(
    private val segments: List<SubtitleSegment>
) {
    fun findActiveSegment(timelineMs: Long): SubtitleSegment? {
        if (segments.isEmpty()) return null

        var lo = 0
        var hi = segments.size - 1
        while (lo <= hi) {
            val mid = (lo + hi) / 2
            val seg = segments[mid]
            val segStartMs = (seg.timeline_start_sec * 1000).toLong()
            val segEndMs = (seg.timeline_end_sec * 1000).toLong()
            when {
                timelineMs < segStartMs -> hi = mid - 1
                timelineMs >= segEndMs -> lo = mid + 1
                else -> return seg
            }
        }
        // No exact match: player outside subtitle range
        return null
    }

    fun findActiveWord(segment: SubtitleSegment, timelineMs: Long): WordEntry? {
        if (segment.words.isEmpty()) return null

        var lo = 0
        var hi = segment.words.size - 1
        var activeWord: WordEntry? = null
        while (lo <= hi) {
            val mid = (lo + hi) / 2
            val word = segment.words[mid]
            val wordStartMs = (word.start_sec * 1000).toLong()
            val wordEndMs = (word.end_sec * 1000).toLong()
            when {
                timelineMs < wordStartMs -> hi = mid - 1
                timelineMs >= wordEndMs -> {
                    activeWord = word
                    lo = mid + 1
                }
                else -> return word
            }
        }
        return activeWord
    }

    fun getWordTimelineMs(word: WordEntry): Long {
        return (word.start_sec * 1000).toLong()
    }

    fun findSegmentForWord(word: WordEntry, allSegments: List<SubtitleSegment>): SubtitleSegment? {
        for (seg in allSegments) {
            if (seg.words.any { it === word }) return seg
        }
        return null
    }
}

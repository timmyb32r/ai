package com.crimobile.sync

import com.crimobile.model.SegmentMeta
import com.crimobile.model.SubtitleSegment
import com.crimobile.model.WordEntry

/**
 * Maps playback timeline position (Unix epoch milliseconds) to the active
 * subtitle segment and word. Uses binary search for O(log n) lookup.
 *
 * Works with lightweight [SegmentMeta] for timeline navigation; word-level
 * resolution requires a fully-loaded [SubtitleSegment] passed directly to
 * [findActiveWord].
 *
 * Timeline correlation: both audio (via windowStartTimeMs from HLS
 * EXT-X-PROGRAM-DATE-TIME) and metadata (via server-side Unix epoch
 * timestamps) share the SAME system clock in the Docker container —
 * a single source of truth.
 */
class SubtitleSyncEngine(
    private val segmentsMeta: List<SegmentMeta>
) {
    fun findActiveSegment(timelineMs: Long): SegmentMeta? {
        if (segmentsMeta.isEmpty()) return null

        var lo = 0
        var hi = segmentsMeta.size - 1
        while (lo <= hi) {
            val mid = (lo + hi) / 2
            val meta = segmentsMeta[mid]
            val segStartMs = (meta.timeline_start_sec * 1000).toLong()
            val segEndMs = (meta.timeline_end_sec * 1000).toLong()
            when {
                timelineMs < segStartMs -> hi = mid - 1
                timelineMs >= segEndMs -> lo = mid + 1
                else -> return meta
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
                timelineMs < wordStartMs -> {
                    // Strictly before this word — search earlier words only.
                    // An upcoming word is NOT active: before the first word there
                    // is no active word (activeWord stays null → returns null).
                    hi = mid - 1
                }
                timelineMs >= wordEndMs -> {
                    // At/after this word's end — it is the most recent word so far;
                    // keep it as the candidate. Covers gaps between words and the
                    // tail after the last word.
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
}

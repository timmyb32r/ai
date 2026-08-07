package com.crimobile.offline

import com.crimobile.model.SegmentMeta

/**
 * Pure, testable mapping between absolute epoch-millisecond timeline positions
 * (used by [com.crimobile.sync.SubtitleSyncEngine]) and local ExoPlayer
 * positions inside the offline concatenated audio stream.
 *
 * Extracted from [OfflineRadioPlayer] so the segment→offset invariant can be
 * unit-tested without a real ExoPlayer. Two bugs lived in the old inline code:
 *
 *  1. **orderedSegments / segmentOffsetsMs desync** — the offset table was
 *     built in iteration order of the incoming segment list, then
 *     `orderedSegments` was re-sorted by `segment_id` while the offset array
 *     stayed in the old order, so `segmentOffsetsMs[i]` no longer described
 *     `orderedSegments[i]`. Seek and timeline mapping silently broke whenever
 *     the server's segment order did not match `segment_id` order.
 *  2. **seekToLiveEdge jumped to the END of the stream** — `segmentOffsetsMs`
 *     has size `n + 1` (its last entry is the *total* duration, i.e. a position
 *     past the final segment), so seeking there put ExoPlayer into STATE_ENDED.
 *
 * Both are fixed here by sorting once by `timeline_start_sec` (chronological —
 * the order segments are added to the ConcatenatingMediaSource and the order
 * the binary searches assume) and exposing [liveEdgePositionMs] as the *start*
 * of the last segment.
 */
class OfflineTimelineMapper(segments: List<SegmentMeta>) {

    /** Segments sorted chronologically by [SegmentMeta.timeline_start_sec]. */
    val orderedSegments: List<SegmentMeta>

    /**
     * Prefix-sum offset table, size `orderedSegments.size + 1`.
     * `segmentOffsetsMs[i]` = total duration before segment `i`;
     * `segmentOffsetsMs.last()` = total duration (a position PAST the final segment).
     */
    val segmentOffsetsMs: LongArray

    init {
        val sorted = segments.sortedBy { it.timeline_start_sec }
        val offsets = mutableListOf(0L)
        for (seg in sorted) {
            val durMs = ((seg.timeline_end_sec - seg.timeline_start_sec) * 1000)
                .toLong().coerceAtLeast(1)
            offsets.add(offsets.last() + durMs)
        }
        orderedSegments = sorted
        segmentOffsetsMs = offsets.toLongArray()
    }

    /** Result of resolving an absolute timeline seek to a local stream position. */
    data class SeekTarget(
        val segmentIndex: Int,
        val offsetInSegmentMs: Long,
        /** Absolute position in the concatenated stream (continuous mode). */
        val absolutePositionMs: Long
    )

    /**
     * Binary search: which segment contains [timelineMs] (absolute epoch ms).
     * Returns the last segment index if [timelineMs] is past the end,
     * or -1 if it is before the first segment / the table is empty.
     */
    fun findSegmentForTimelineMs(timelineMs: Long): Int {
        if (orderedSegments.isEmpty()) return -1
        var lo = 0
        var hi = orderedSegments.size - 1
        while (lo <= hi) {
            val mid = (lo + hi) / 2
            val seg = orderedSegments[mid]
            val segStart = (seg.timeline_start_sec * 1000).toLong()
            val segEnd = (seg.timeline_end_sec * 1000).toLong()
            when {
                timelineMs < segStart -> hi = mid - 1
                timelineMs >= segEnd -> lo = mid + 1
                else -> return mid
            }
        }
        if (lo >= orderedSegments.size) return orderedSegments.size - 1
        if (hi < 0) return -1
        return hi
    }

    /**
     * Binary search: which segment contains local concat [positionMs].
     * Returns -1 if out of range / the table is empty.
     */
    fun findSegmentForPosition(positionMs: Long): Int {
        if (orderedSegments.isEmpty()) return -1
        var lo = 0
        var hi = orderedSegments.size - 1
        while (lo <= hi) {
            val mid = (lo + hi) / 2
            val segStart = segmentOffsetsMs[mid]
            val segEnd = segmentOffsetsMs[mid + 1]
            when {
                positionMs < segStart -> hi = mid - 1
                positionMs >= segEnd -> lo = mid + 1
                else -> return mid
            }
        }
        return -1
    }

    /**
     * Local concat position of the offline "live edge" — the START of the last
     * segment, NOT the end (seeking to the end puts ExoPlayer into STATE_ENDED).
     */
    fun liveEdgePositionMs(): Long {
        if (orderedSegments.isEmpty()) return 0L
        return segmentOffsetsMs[orderedSegments.lastIndex]
    }

    /** Absolute timeline ms for a local concat [positionMs]. */
    fun timelineMsForPosition(positionMs: Long): Long {
        val idx = findSegmentForPosition(positionMs)
        if (idx < 0) return 0L
        val seg = orderedSegments[idx]
        val offsetInSeg = positionMs - segmentOffsetsMs[idx]
        return (seg.timeline_start_sec * 1000).toLong() + offsetInSeg
    }

    /** Resolve an absolute timeline seek to a local stream target. */
    fun seekTarget(timelineMs: Long): SeekTarget {
        val idx = findSegmentForTimelineMs(timelineMs)
        if (idx < 0) return SeekTarget(0, 0L, 0L)
        val seg = orderedSegments[idx]
        val segStartMs = (seg.timeline_start_sec * 1000).toLong()
        val segDurMs = ((seg.timeline_end_sec - seg.timeline_start_sec) * 1000).toLong()
        val offsetInSeg = (timelineMs - segStartMs).coerceIn(0, segDurMs)
        val absolutePos = (segmentOffsetsMs[idx] + offsetInSeg).coerceAtLeast(0)
        return SeekTarget(idx, offsetInSeg, absolutePos)
    }
}

package com.crimobile.offline

import com.crimobile.model.SegmentMeta
import org.junit.Assert.*
import org.junit.Test

/**
 * Regression tests for the offline timeline mapping.
 *
 * Background: [OfflineRadioPlayer] previously built the prefix-sum offset table
 * in iteration order of the incoming segment list and then re-sorted
 * `orderedSegments` by `segment_id` while leaving the offset array in the old
 * order — so `segmentOffsetsMs[i]` no longer described `orderedSegments[i]` when
 * the server's order did not match `segment_id` order. `seekToLiveEdge` also
 * jumped to `segmentOffsetsMs.last()` (the *total* duration, a position past the
 * final segment → STATE_ENDED). [OfflineTimelineMapper] fixes both.
 */
class OfflineTimelineMapperTest {

    private fun seg(id: Int, start: Double, end: Double) =
        SegmentMeta(id, start, end, "$id.ts", "zh$id", "py$id")

    @Test
    fun `offsets stay in sync with orderedSegments when input is unsorted`() {
        // Input is in segment_id order desc, but timeline asc — the mapper must
        // sort by timeline_start_sec so the offset table and the ordered list agree.
        val input = listOf(seg(500, 100.0, 103.0), seg(100, 103.0, 106.0), seg(300, 106.0, 109.0))
        val m = OfflineTimelineMapper(input)

        assertEquals(listOf(100, 103, 106), m.orderedSegments.map { it.timeline_start_sec.toInt() })
        assertArrayEquals(longArrayOf(0, 3000, 6000, 9000), m.segmentOffsetsMs)

        // Each segment's duration must equal the gap between consecutive offsets.
        for (i in m.orderedSegments.indices) {
            val segStart = (m.orderedSegments[i].timeline_start_sec * 1000).toLong()
            val segEnd = (m.orderedSegments[i].timeline_end_sec * 1000).toLong()
            assertEquals(
                "offset gap for segment $i",
                segEnd - segStart,
                m.segmentOffsetsMs[i + 1] - m.segmentOffsetsMs[i]
            )
        }
    }

    @Test
    fun `liveEdgePositionMs is start of last segment, not total duration`() {
        val m = OfflineTimelineMapper(listOf(seg(1, 100.0, 103.0), seg(2, 103.0, 106.0)))
        // Start of the last segment = 3000ms; total duration (old buggy value) = 6000ms.
        assertEquals(3000L, m.liveEdgePositionMs())
    }

    @Test
    fun `seekTarget maps timeline to correct segment and absolute position`() {
        val input = listOf(seg(500, 100.0, 103.0), seg(100, 103.0, 106.0), seg(300, 106.0, 109.0))
        val m = OfflineTimelineMapper(input)

        val t = m.seekTarget(104_000L) // inside seg(100): [103000, 106000)
        assertEquals(1, t.segmentIndex)
        assertEquals(1000L, t.offsetInSegmentMs)
        assertEquals(4000L, t.absolutePositionMs) // offsets[1]=3000 (after seg500) + 1000
    }

    @Test
    fun `timelineMsForPosition round-trips through seekTarget`() {
        val m = OfflineTimelineMapper(listOf(seg(500, 100.0, 103.0), seg(100, 103.0, 106.0), seg(300, 106.0, 109.0)))
        for (timelineMs in listOf(101_000L, 104_500L, 107_999L)) {
            val t = m.seekTarget(timelineMs)
            assertEquals("round-trip for $timelineMs", timelineMs, m.timelineMsForPosition(t.absolutePositionMs))
        }
    }

    @Test
    fun `findSegmentForTimelineMs clamps past end to last and returns -1 before start`() {
        val m = OfflineTimelineMapper(listOf(seg(1, 100.0, 103.0), seg(2, 103.0, 106.0)))
        assertEquals(-1, m.findSegmentForTimelineMs(99_000L)) // before first
        assertEquals(0, m.findSegmentForTimelineMs(101_000L)) // in first
        assertEquals(1, m.findSegmentForTimelineMs(104_000L)) // in second
        assertEquals(1, m.findSegmentForTimelineMs(200_000L)) // past end → last
    }

    @Test
    fun `empty input yields empty segments and safe positions`() {
        val m = OfflineTimelineMapper(emptyList())
        assertTrue(m.orderedSegments.isEmpty())
        assertEquals(1, m.segmentOffsetsMs.size) // just [0]
        assertEquals(0L, m.liveEdgePositionMs())
        assertEquals(-1, m.findSegmentForTimelineMs(1000L))
        assertEquals(0L, m.timelineMsForPosition(1000L))
    }
}

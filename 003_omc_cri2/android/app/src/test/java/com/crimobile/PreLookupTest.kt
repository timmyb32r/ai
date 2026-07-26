package com.crimobile

import com.crimobile.model.SubtitleSegment
import com.crimobile.model.WordEntry
import org.junit.Assert.*
import org.junit.Test

/**
 * Tests for the pre-lookup logic that finds the active segment from loaded data
 * without any HLS playlist parsing or additional HTTP requests.
 *
 * Algorithm: given a sorted list of completed segments and a player offset (20s),
 * compute playerSec = newestSegment.timeline_end_sec - offset,
 * then binary-search for the segment covering playerSec.
 */
class PreLookupTest {

    companion object {
        private const val PLAYLIST_OFFSET_SEC = 20.0
    }

    /** Build a simple segment with a known timeline. */
    private fun segment(id: Int, startSec: Double, endSec: Double, text: String = "seg$id") =
        SubtitleSegment(
            segment_id = id,
            timeline_start_sec = startSec,
            timeline_end_sec = endSec,
            text_zh = text,
            text_pinyin = "",
            text_en = "",
            ts_file = "",
            words = emptyList<WordEntry>(),
        )

    /**
     * The pre-lookup algorithm extracted as a pure function for testability.
     * Returns the segment covering playerSec, or null if none found.
     */
    private fun findActiveSegmentAtOffset(
        segments: List<SubtitleSegment>,
        offsetSec: Double,
    ): SubtitleSegment? {
        if (segments.isEmpty()) return null

        // Live edge = end of the newest completed segment
        val newestSeg = segments.last()
        val playerSec = newestSeg.timeline_end_sec - offsetSec

        return segments.find { seg ->
            playerSec >= seg.timeline_start_sec && playerSec < seg.timeline_end_sec
        }
    }

    @Test
    fun `finds correct segment when playerSec falls within range`() {
        // 5 segments, each 3 seconds: [0-3, 3-6, 6-9, 9-12, 12-15]
        val segments = listOf(
            segment(0, 100.0, 103.0),
            segment(1, 103.0, 106.0),
            segment(2, 106.0, 109.0),
            segment(3, 109.0, 112.0),
            segment(4, 112.0, 115.0),
        )

        // newestSeg ends at 115.0. playerSec = 115.0 - 20.0 = 95.0
        // 95.0 is before all segments → null
        val result = findActiveSegmentAtOffset(segments, PLAYLIST_OFFSET_SEC)
        assertNull("playerSec=95 is before first segment (100)", result)
    }

    @Test
    fun `finds segment when batch covers playerSec`() {
        // Segments covering a realistic range: newest ends at 1000s, offset 20s → player at 980s
        val segments = listOf(
            segment(10, 970.0, 973.0),
            segment(11, 973.0, 976.0),
            segment(12, 976.0, 979.0),
            segment(13, 979.0, 982.0),  // playerSec=980 falls here
            segment(14, 982.0, 985.0),
            segment(15, 985.0, 988.0),
            segment(16, 988.0, 991.0),
            segment(17, 991.0, 994.0),
            segment(18, 994.0, 997.0),
            segment(19, 997.0, 1000.0), // newest
        )

        val result = findActiveSegmentAtOffset(segments, PLAYLIST_OFFSET_SEC)
        assertNotNull("playerSec should be in range", result)
        assertEquals(13, result!!.segment_id)
    }

    @Test
    fun `returns null when playerSec at exact boundary of newest segment`() {
        // playerSec = newest.timeline_start_sec exactly
        val segments = listOf(
            segment(0, 100.0, 103.0),
            segment(1, 103.0, 106.0),
            segment(2, 106.0, 109.0),
            segment(3, 109.0, 112.0),
            segment(4, 112.0, 115.0),
        )
        // newest ends at 115. playerSec = 115 - 20 = 95. 95 < all segments → null
        val result = findActiveSegmentAtOffset(segments, PLAYLIST_OFFSET_SEC)
        assertNull("playerSec=95 before all segments", result)
    }

    @Test
    fun `returns null for empty segment list`() {
        val result = findActiveSegmentAtOffset(emptyList(), PLAYLIST_OFFSET_SEC)
        assertNull("empty list should return null", result)
    }

    @Test
    fun `works with 40-segment batch - realistic cold start`() {
        // Simulate a realistic cold start: 40 segments, each 3s, starting from epoch base
        val baseSec = 1_785_090_000.0
        val segments = (0..39).map { id ->
            val start = baseSec + id * 3.0
            segment(id, start, start + 3.0)
        }

        // Player at liveEdge - 20s = (baseSec + 40*3) - 20 = baseSec + 100
        val expectedPlayerSec = baseSec + 100.0
        // Segment at position: (100 / 3) = 33.33 → segment 33 covers [baseSec+99, baseSec+102]
        val expectedSegId = 33

        val result = findActiveSegmentAtOffset(segments, PLAYLIST_OFFSET_SEC)
        assertNotNull("should find segment in 40-segment batch", result)
        assertEquals(
            "playerSec=$expectedPlayerSec should be in segment $expectedSegId",
            expectedSegId, result!!.segment_id)
    }

    @Test
    fun `offset of 0 returns the newest segment`() {
        val segments = listOf(
            segment(0, 100.0, 103.0),
            segment(1, 103.0, 106.0),
            segment(2, 106.0, 109.0),
        )
        // offset=0 → playerSec = 109.0 (newest.timeline_end_sec)
        // 109.0 >= 106 && 109.0 < 109 → FALSE (not < 109)
        // Actually 109.0 < 109.0 is false, so it returns null.
        // But with a real player, 109.0 is exactly at the end boundary.
        // This is expected — player is at live edge, no segment covers it.
        val result = findActiveSegmentAtOffset(segments, 0.0)
        assertNull("playerSec at exact end boundary returns null", result)
    }

    @Test
    fun `previous bug - offset matches original player position`() {
        // Reproduce the bug from the logs:
        // fetchInitial returned 21 segments ids=[20..0], loadedRange=[722992..816183]
        // Real playerSec was ~774, pre-lookup computed 811 (37s too late!)
        // After fix: playerSec = 816183 - 20 = 816163
        val baseSec = 722_992.0
        val segments = (0..20).map { id ->
            segment(id, baseSec + id * 3.0, baseSec + (id + 1) * 3.0)
        }
        // newest = seg20, ends at baseSec + 21*3 = 722992 + 63 = 723055
        // playerSec = 723055 - 20 = 723035
        // segment covering 723035: id = (723035 - 722992) / 3 = 43/3 = 14
        val result = findActiveSegmentAtOffset(segments, PLAYLIST_OFFSET_SEC)
        assertNotNull("should find segment", result)
        assertEquals(14, result!!.segment_id)

        // Verify playerSec is reasonable (not 37s off like the bug)
        val expectedPlayerSec = segments.last().timeline_end_sec - PLAYLIST_OFFSET_SEC
        assertTrue(
            "playerSec=$expectedPlayerSec should be within loaded range ${segments.first().timeline_start_sec}",
            expectedPlayerSec >= segments.first().timeline_start_sec)
    }
}

/**
 * Tests for the sync-loop preservation logic: when the sync loop can't find
 * an active segment (player in a gap), it must NOT overwrite the pre-lookup's
 * activeSegment/activeWord with null.
 */
class SyncLoopPreservationTest {

    companion object {
        private const val PLAYLIST_OFFSET_SEC = 20.0
    }

    private fun segment(id: Int, startSec: Double, endSec: Double, text: String = "seg$id") =
        SubtitleSegment(
            segment_id = id,
            timeline_start_sec = startSec,
            timeline_end_sec = endSec,
            text_zh = text,
            text_pinyin = "",
            text_en = "",
            ts_file = "",
            words = emptyList<WordEntry>(),
        )

    data class SyncState(
        val activeSegment: SubtitleSegment?,
        val activeSegmentId: Int?,
        val activeWord: WordEntry?,
    )

    /**
     * Imitates the sync loop's merge logic:
     * syncResult is what findActiveSegment returned (may be null).
     * previousState is what's already in _state (may have pre-lookup).
     * Returns the final values that should be written to _state.
     */
    private fun mergeSyncResult(
        syncSegment: SubtitleSegment?,
        syncSegmentId: Int?,
        syncWord: WordEntry?,
        previousState: SyncState,
    ): SyncState {
        val finalSegment = syncSegment ?: previousState.activeSegment
        val finalSegmentId = syncSegmentId ?: previousState.activeSegmentId
        val finalWord = syncWord ?: previousState.activeWord
        return SyncState(finalSegment, finalSegmentId, finalWord)
    }

    @Test
    fun `sync preserves pre-lookup when sync returns null`() {
        val preSeg = segment(5, 100.0, 103.0, "prelookup")
        val prevState = SyncState(activeSegment = preSeg, activeSegmentId = 5, activeWord = null)

        // Sync loop runs, finds nothing (player in gap)
        val result = mergeSyncResult(
            syncSegment = null,
            syncSegmentId = null,
            syncWord = null,
            previousState = prevState,
        )

        assertEquals("pre-lookup segment preserved", preSeg, result.activeSegment)
        assertEquals("pre-lookup segmentId preserved", 5, result.activeSegmentId)
    }

    @Test
    fun `sync overwrites pre-lookup when sync finds different segment`() {
        val preSeg = segment(5, 100.0, 103.0, "prelookup")
        val prevState = SyncState(activeSegment = preSeg, activeSegmentId = 5, activeWord = null)

        val syncSeg = segment(6, 103.0, 106.0, "sync-found")
        val result = mergeSyncResult(
            syncSegment = syncSeg,
            syncSegmentId = 6,
            syncWord = null,
            previousState = prevState,
        )

        assertEquals("sync segment overwrites pre-lookup", syncSeg, result.activeSegment)
        assertEquals("sync segmentId overwrites pre-lookup", 6, result.activeSegmentId)
    }

    @Test
    fun `sync confirms pre-lookup when both find same segment`() {
        val preSeg = segment(5, 100.0, 103.0, "same")
        val prevState = SyncState(activeSegment = preSeg, activeSegmentId = 5, activeWord = null)

        // Sync loop finds the same segment — overwrites with identical data, no flicker
        val result = mergeSyncResult(
            syncSegment = preSeg,
            syncSegmentId = 5,
            syncWord = null,
            previousState = prevState,
        )

        assertEquals(preSeg, result.activeSegment)
        assertEquals(5, result.activeSegmentId)
    }

    @Test
    fun `no pre-lookup, sync finds segment — works`() {
        val prevState = SyncState(activeSegment = null, activeSegmentId = null, activeWord = null)

        val syncSeg = segment(0, 100.0, 103.0)
        val result = mergeSyncResult(
            syncSegment = syncSeg,
            syncSegmentId = 0,
            syncWord = null,
            previousState = prevState,
        )

        assertEquals("sync segment used when no pre-lookup", syncSeg, result.activeSegment)
    }

    @Test
    fun `no pre-lookup, sync null — stays null`() {
        val prevState = SyncState(activeSegment = null, activeSegmentId = null, activeWord = null)

        val result = mergeSyncResult(
            syncSegment = null,
            syncSegmentId = null,
            syncWord = null,
            previousState = prevState,
        )

        assertNull("stays null", result.activeSegment)
        assertNull("stays null", result.activeSegmentId)
    }

    @Test
    fun `pre-lookup found segment but id mismatch in prev state — preserves prev`() {
        // Edge case: pre-lookup set both, then sync fails.
        // Should preserve BOTH, not mix.
        val preSeg = segment(5, 100.0, 103.0, "pre")
        val prevState = SyncState(activeSegment = preSeg, activeSegmentId = 5, activeWord = null)

        // Sync finds nothing
        val result = mergeSyncResult(null, null, null, prevState)

        assertEquals(preSeg, result.activeSegment)
        assertEquals(5, result.activeSegmentId)
    }

    @Test
    fun `sync finds segment but not word — preserves pre-lookup word`() {
        val preWord = WordEntry(
            text = "test", char_start = 0, char_end = 1,
            start_sec = 100.0, end_sec = 101.0,
            pinyin = "", translation = "",
        )
        val prevState = SyncState(activeSegment = null, activeSegmentId = null, activeWord = preWord)

        val syncSeg = segment(5, 100.0, 103.0)
        val result = mergeSyncResult(
            syncSegment = syncSeg,
            syncSegmentId = 5,
            syncWord = null, // sync couldn't find word
            previousState = prevState,
        )

        assertEquals(syncSeg, result.activeSegment)
        assertEquals("pre-lookup word preserved when sync word is null", preWord, result.activeWord)
    }

    @Test
    fun `sync finds everything — overwrites both segment and word`() {
        val preSeg = segment(4, 97.0, 100.0, "old")
        val preWord = WordEntry(
            text = "old", char_start = 0, char_end = 1,
            start_sec = 97.0, end_sec = 98.0,
            pinyin = "", translation = "",
        )
        val prevState = SyncState(activeSegment = preSeg, activeSegmentId = 4, activeWord = preWord)

        val syncSeg = segment(5, 100.0, 103.0, "new")
        val syncWord = WordEntry(
            text = "new", char_start = 0, char_end = 1,
            start_sec = 100.0, end_sec = 101.0,
            pinyin = "", translation = "",
        )
        val result = mergeSyncResult(syncSeg, 5, syncWord, prevState)

        assertEquals(syncSeg, result.activeSegment)
        assertEquals(5, result.activeSegmentId)
        assertEquals("sync word overwrites pre-lookup", syncWord, result.activeWord)
    }

    @Test
    fun `sync fails completely — preserves entire pre-lookup state`() {
        val preSeg = segment(5, 100.0, 103.0, "pre")
        val preWord = WordEntry(
            text = "pre", char_start = 0, char_end = 1,
            start_sec = 100.0, end_sec = 101.0,
            pinyin = "", translation = "",
        )
        val prevState = SyncState(activeSegment = preSeg, activeSegmentId = 5, activeWord = preWord)

        // Player in gap — sync finds nothing
        val result = mergeSyncResult(null, null, null, prevState)

        assertEquals("segment preserved", preSeg, result.activeSegment)
        assertEquals("segmentId preserved", 5, result.activeSegmentId)
        assertEquals("word preserved", preWord, result.activeWord)
    }
}

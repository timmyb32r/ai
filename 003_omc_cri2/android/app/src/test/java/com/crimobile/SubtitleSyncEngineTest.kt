package com.crimobile

import com.crimobile.model.SubtitleSegment
import com.crimobile.model.WordEntry
import com.crimobile.sync.SubtitleSyncEngine
import org.junit.Assert.*
import org.junit.Test

/**
 * Tests that verify the complete path of subtitle data:
 *   segment arrival → timeline mapping → word highlighting.
 *
 * Any change that breaks the word-level timeline alignment MUST fail these tests.
 */
class SubtitleSyncEngineTest {

    // Real data from server: segment 0, timeline 1,781,645,178.332 to 1,781,645,181.332
    private val sampleSegment = SubtitleSegment(
        segment_id = 0,
        timeline_start_sec = 1_781_645_178.332,
        timeline_end_sec = 1_781_645_181.332,
        ts_file = "000000000.ts",
        text_zh = "東南部等地部分地區有大刀暴雨",
        text_pinyin = "",
        text_en = "",
        words = listOf(
            WordEntry("東南部", 0, 3, 1_781_645_178.332, 1_781_645_178.974857, "", ""),
            WordEntry("等", 3, 4, 1_781_645_178.974857, 1_781_645_179.1891427, "", ""),
            WordEntry("地", 4, 5, 1_781_645_179.1891427, 1_781_645_179.4034283, "", ""),
            WordEntry("部分", 5, 7, 1_781_645_179.4034283, 1_781_645_179.8319998, "", ""),
            WordEntry("地區", 7, 9, 1_781_645_179.8319998, 1_781_645_180.2605712, "", ""),
            WordEntry("有", 9, 10, 1_781_645_180.2605712, 1_781_645_180.4748569, "", ""),
            WordEntry("大刀", 10, 12, 1_781_645_180.4748569, 1_781_645_180.9034283, "", ""),
            WordEntry("暴雨", 12, 14, 1_781_645_180.9034283, 1_781_645_181.3319998, "", "")
        )
    )

    // ── Segment-level timeline tests ───────────────────────────────────

    @Test
    fun `findActiveSegment — player inside segment`() {
        val engine = SubtitleSyncEngine(listOf(sampleSegment))
        // Player at ~1.5 seconds into the segment (absolute ms)
        val playerMs = ((sampleSegment.timeline_start_sec + 1.5) * 1000).toLong()
        val result = engine.findActiveSegment(playerMs)
        assertNotNull("segment should be found when player is inside it", result)
        assertEquals(0, result!!.segment_id)
    }

    @Test
    fun `findActiveSegment — player exactly at segment start`() {
        val engine = SubtitleSyncEngine(listOf(sampleSegment))
        val playerMs = (sampleSegment.timeline_start_sec * 1000).toLong()
        val result = engine.findActiveSegment(playerMs)
        assertNotNull("segment should be found at exact start boundary", result)
        assertEquals(0, result!!.segment_id)
    }

    @Test
    fun `findActiveSegment — player before all segments returns null`() {
        val engine = SubtitleSyncEngine(listOf(sampleSegment))
        val playerMs = ((sampleSegment.timeline_start_sec - 10.0) * 1000).toLong()
        val result = engine.findActiveSegment(playerMs)
        assertNull("player before all segments should return null", result)
    }

    @Test
    fun `findActiveSegment — player after all segments returns null`() {
        val engine = SubtitleSyncEngine(listOf(sampleSegment))
        val playerMs = ((sampleSegment.timeline_end_sec + 10.0) * 1000).toLong()
        val result = engine.findActiveSegment(playerMs)
        assertNull("player after all segments should return null", result)
    }

    @Test
    fun `findActiveSegment — multiple segments, binary search correctness`() {
        val segments = (0..9).map { id ->
            sampleSegment.copy(
                segment_id = id,
                timeline_start_sec = sampleSegment.timeline_start_sec + id * 3.0,
                timeline_end_sec = sampleSegment.timeline_end_sec + id * 3.0
            )
        }
        val engine = SubtitleSyncEngine(segments)

        // Player at segment 5, 1 second in
        val playerMs = ((segments[5].timeline_start_sec + 1.0) * 1000).toLong()
        val result = engine.findActiveSegment(playerMs)
        assertNotNull(result)
        assertEquals(5, result!!.segment_id)
    }

    // ── Word-level timeline tests (CRITICAL — must survive refactoring) ─

    @Test
    fun `findActiveWord — first word of segment`() {
        val engine = SubtitleSyncEngine(listOf(sampleSegment))
        // Player at 0.2s into the segment
        val playerMs = ((sampleSegment.timeline_start_sec + 0.2) * 1000).toLong()
        val word = engine.findActiveWord(sampleSegment, playerMs)
        assertNotNull("should find a word", word)
        assertEquals("東南部", word!!.text)
    }

    @Test
    fun `findActiveWord — middle word of segment`() {
        val engine = SubtitleSyncEngine(listOf(sampleSegment))
        // Player at 2.0s into segment — should be "地區" (starts at 1.5s relative)
        val playerMs = ((sampleSegment.timeline_start_sec + 2.0) * 1000).toLong()
        val word = engine.findActiveWord(sampleSegment, playerMs)
        assertNotNull(word)
        assertEquals("地區", word!!.text)
    }

    @Test
    fun `findActiveWord — last word of segment`() {
        val engine = SubtitleSyncEngine(listOf(sampleSegment))
        // Player at 2.9s — should be "暴雨" (last word, starts at 2.57s)
        val playerMs = ((sampleSegment.timeline_start_sec + 2.9) * 1000).toLong()
        val word = engine.findActiveWord(sampleSegment, playerMs)
        assertNotNull(word)
        assertEquals("暴雨", word!!.text)
    }

    @Test
    fun `findActiveWord — between words returns previous word`() {
        val engine = SubtitleSyncEngine(listOf(sampleSegment))
        // "等" ends at rel 0.857s, "地" starts at 0.857s
        // Player at 1.0s — should be "地"
        val playerMs = ((sampleSegment.timeline_start_sec + 1.0) * 1000).toLong()
        val word = engine.findActiveWord(sampleSegment, playerMs)
        assertNotNull(word)
        assertEquals("地", word!!.text)
    }

    @Test
    fun `findActiveWord — before first word returns null`() {
        val engine = SubtitleSyncEngine(listOf(sampleSegment))
        val playerMs = ((sampleSegment.timeline_start_sec - 0.5) * 1000).toLong()
        val word = engine.findActiveWord(sampleSegment, playerMs)
        assertNull("before first word should return null", word)
    }

    @Test
    fun `findActiveWord — after last word returns last word`() {
        val engine = SubtitleSyncEngine(listOf(sampleSegment))
        val playerMs = ((sampleSegment.timeline_end_sec + 0.5) * 1000).toLong()
        val word = engine.findActiveWord(sampleSegment, playerMs)
        assertNotNull("after segment should return last word", word)
        assertEquals("暴雨", word!!.text)
    }

    // ── Timeline continuity tests ──────────────────────────────────────

    @Test
    fun `word timestamps are non-decreasing within segment`() {
        for (i in 1 until sampleSegment.words.size) {
            val prev = sampleSegment.words[i - 1]
            val curr = sampleSegment.words[i]
            assertTrue(
                "word[$i] start_sec (${curr.start_sec}) must be >= word[${i - 1}] end_sec (${prev.end_sec})",
                curr.start_sec >= prev.end_sec
            )
            assertTrue(
                "word[$i] end_sec (${curr.end_sec}) must be > word[$i] start_sec (${curr.start_sec})",
                curr.end_sec > curr.start_sec
            )
        }
    }

    @Test
    fun `first word starts at segment start`() {
        val firstWord = sampleSegment.words.first()
        assertEquals(
            "first word start must equal segment start",
            sampleSegment.timeline_start_sec,
            firstWord.start_sec,
            0.001
        )
    }

    @Test
    fun `last word ends at segment end`() {
        val lastWord = sampleSegment.words.last()
        assertEquals(
            "last word end must equal segment end",
            sampleSegment.timeline_end_sec,
            lastWord.end_sec,
            0.001
        )
    }

    // ── Timeline sync: player position ↔ segment/word mapping ─────────

    @Test
    fun `complete sync chain — playerMs maps to correct segment and word`() {
        val segments = (0..4).map { id ->
            sampleSegment.copy(
                segment_id = id,
                timeline_start_sec = sampleSegment.timeline_start_sec + id * 3.0,
                timeline_end_sec = sampleSegment.timeline_end_sec + id * 3.0
            )
        }
        val engine = SubtitleSyncEngine(segments)

        // Player at segment 2, position ≈ 1.2 seconds in → should be "地"
        val playerMs = ((segments[2].timeline_start_sec + 1.2) * 1000).toLong()
        val seg = engine.findActiveSegment(playerMs)
        assertNotNull("segment must be found", seg)
        assertEquals(2, seg!!.segment_id)

        val word = engine.findActiveWord(seg, playerMs)
        assertNotNull("word must be found", word)
        assertEquals("地", word!!.text)

        // Verify the word's absolute timeline contains the player position
        val playerSec = playerMs / 1000.0
        assertTrue(
            "playerSec $playerSec should be >= word start ${word.start_sec}",
            playerSec >= word.start_sec
        )
    }

    @Test
    fun `word absolute timestamps are in Unix epoch seconds`() {
        // Server uses Unix epoch — timestamps should be large (~1.78e9 in 2026)
        for (word in sampleSegment.words) {
            assertTrue(
                "word.start_sec (${word.start_sec}) should be > 1.7e9 (Unix epoch 2024+)",
                word.start_sec > 1_700_000_000.0
            )
            assertTrue(
                "word.end_sec (${word.end_sec}) should be > 1.7e9",
                word.end_sec > 1_700_000_000.0
            )
        }
    }
}

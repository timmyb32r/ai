package com.crimobile

import com.crimobile.api.HighlightTracker
import com.crimobile.model.SubtitleEvent
import com.crimobile.model.WordBoundary
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * Tests for the absolute-timeline subtitle highlighting introduced by the
 * sync-event fix.
 *
 * The core function under test is [HighlightTracker.calculateHighlightIndex],
 * which is reused by the new absolute-timeline path.  These tests verify that
 * when given absolute (not relative) values it produces correct character
 * indices for realistic broadcast-timeline positions.
 */
class CriViewModelTest {

    private fun calc(
        relAudioSec: Double,
        relStart: Double,
        relEnd: Double,
        textLength: Int,
    ): Int = HighlightTracker.calculateHighlightIndex(relAudioSec, relStart, relEnd, textLength)

    // ── Sync-event: absolute-timeline mode ──────────────────────────────────

    // When audioTimelineStart = 824.0 and the MediaPlayer position is
    // 8000 ms, the absolute broadcast timeline is 824.0 + 8.0 = 832.0.
    // A subtitle with Start=832.0/End=840.0 should highlight character 0.

    @Test
    fun `sync mode — highlight at window start`() {
        val audioTimelineStart = 824.0
        val posMs = 8000L // 8 s into playback
        val absTimeline = audioTimelineStart + posMs / 1000.0 // = 832.0

        val sub = SubtitleEvent(start = 832.0, end = 840.0, textZh = "一二三四五六七八九十一二三四五六七八九十")
        assertEquals(0, calc(absTimeline, sub.start, sub.end, sub.textZh.length))
    }

    @Test
    fun `sync mode — highlight at 50 percent`() {
        val audioTimelineStart = 824.0
        val posMs = 12000L // 12 s into playback
        val absTimeline = audioTimelineStart + posMs / 1000.0 // = 836.0

        val sub = SubtitleEvent(start = 832.0, end = 840.0, textZh = "一二三四五六七八九十一二三四五六七八九十")
        // 836.0 is 4.0/8.0 = 50 % through [832, 840]
        assertEquals(10, calc(absTimeline, sub.start, sub.end, sub.textZh.length))
    }

    @Test
    fun `sync mode — highlight at window end`() {
        val audioTimelineStart = 824.0
        val posMs = 16000L // 16 s
        val absTimeline = audioTimelineStart + posMs / 1000.0 // = 840.0

        // 20-character string → indices 0..19, last char at 19
        val sub = SubtitleEvent(start = 832.0, end = 840.0, textZh = "一二三四五六七八九十一二三四五六七八九十")
        assertEquals(19, calc(absTimeline, sub.start, sub.end, sub.textZh.length))
    }

    @Test
    fun `sync mode — before window returns -1`() {
        val audioTimelineStart = 824.0
        val posMs = 7000L // 7 s — absTimeline = 831.0, window starts at 832.0
        val absTimeline = audioTimelineStart + posMs / 1000.0

        val sub = SubtitleEvent(start = 832.0, end = 840.0, textZh = "测试")
        assertEquals(-1, calc(absTimeline, sub.start, sub.end, sub.textZh.length))
    }

    @Test
    fun `sync mode — after window returns -1`() {
        val audioTimelineStart = 824.0
        val posMs = 17000L // 17 s — absTimeline = 841.0, window ended at 840.0
        val absTimeline = audioTimelineStart + posMs / 1000.0

        val sub = SubtitleEvent(start = 832.0, end = 840.0, textZh = "测试")
        assertEquals(-1, calc(absTimeline, sub.start, sub.end, sub.textZh.length))
    }

    // ── Zero audioTimelineStart ─────────────────────────────────────────────

    @Test
    fun `sync mode — zero timeline start`() {
        // audioTimelineStart == 0 means the broadcast timeline began exactly at
        // MediaPlayer position 0. This is the cold-start / epoch-start case.
        val audioTimelineStart = 0.0
        val posMs = 4000L // 4 s
        val absTimeline = audioTimelineStart + posMs / 1000.0 // = 4.0

        val sub = SubtitleEvent(start = 0.0, end = 8.0, textZh = "一二三四五六七八九十一二三四五六七八九十")
        assertEquals(10, calc(absTimeline, sub.start, sub.end, sub.textZh.length))
    }

    // ── Fallback mode: no sync event ────────────────────────────────────────

    // When no sync event is received the fallback estimates
    //   absTimeline = (firstSub.start - 4.0) + posMs / 1000.0
    // This assumes audio started roughly mid-window (~4 s before the first
    // subtitle boundary). The estimate is imprecise (±4 s) but keeps the
    // highlight functional.

    @Test
    fun `fallback mode — estimated timeline gives valid highlight`() {
        val firstSubStart = 824.0
        val estimatedStart = firstSubStart - 4.0 // 820.0
        val posMs = 12000L // 12 s
        val absTimeline = estimatedStart + posMs / 1000.0 // = 832.0

        val sub = SubtitleEvent(start = 824.0, end = 832.0, textZh = "一二三四五六七八九十一二三四五六七八九十")
        // At absTimeline=832.0 the window [824,832] has just ended (last char).
        assertEquals(19, calc(absTimeline, sub.start, sub.end, sub.textZh.length))
    }

    @Test
    fun `fallback mode — mid-window highlights correctly`() {
        val estimatedStart = 824.0 - 4.0 // 820.0
        val posMs = 6000L // 6 s
        val absTimeline = estimatedStart + posMs / 1000.0 // = 826.0

        val sub = SubtitleEvent(start = 824.0, end = 832.0, textZh = "一二三四五六七八九十一二三四五六七八九十")
        // 826.0 is 2.0/8.0 = 25 % through [824,832]
        assertEquals(5, calc(absTimeline, sub.start, sub.end, sub.textZh.length))
    }

    // ── Subtitle lookup (lastOrNull by start) ───────────────────────────────

    // The new absolute-timeline loop finds curSub via
    //   subtitleBuffer.lastOrNull { it.start <= absTimeline }
    // This test verifies the lookup logic picks the correct subtitle when
    // multiple contiguous windows are buffered.

    @Test
    fun `subtitle lookup picks correct window`() {
        val subs = listOf(
            SubtitleEvent(start = 100.0, end = 108.0, textZh = "第一段"),
            SubtitleEvent(start = 108.0, end = 116.0, textZh = "第二段"),
            SubtitleEvent(start = 116.0, end = 124.0, textZh = "第三段"),
        )
        val absTimeline = 112.0 // inside second window

        val cur = subs.lastOrNull { it.start <= absTimeline }
        assertEquals("第二段", cur?.textZh)
    }

    @Test
    fun `subtitle lookup at window boundary picks later window`() {
        val subs = listOf(
            SubtitleEvent(start = 100.0, end = 108.0, textZh = "第一段"),
            SubtitleEvent(start = 108.0, end = 116.0, textZh = "第二段"),
        )
        // At exactly the boundary, lastOrNull returns the second entry.
        val cur = subs.lastOrNull { it.start <= 108.0 }
        assertEquals("第二段", cur?.textZh)
    }

    @Test
    fun `subtitle lookup before first window returns null`() {
        val subs = listOf(
            SubtitleEvent(start = 100.0, end = 108.0, textZh = "第一段"),
        )
        val cur = subs.lastOrNull { it.start <= 50.0 }
        assertEquals(null, cur)
    }

    // ── Word-range highlighting ────────────────────────────────────────────

    private fun wordRange(
        relAudioSec: Double,
        relStart: Double,
        relEnd: Double,
        textLength: Int,
        words: List<WordBoundary>?,
    ): IntRange? = HighlightTracker.findWordRange(relAudioSec, relStart, relEnd, textLength, words)

    @Test
    fun `word boundary — highlights entire word`() {
        val words = listOf(WordBoundary(0, 2), WordBoundary(2, 4))
        // 25 % into the window → charIndex = 1 → word [0,2) → range 0..1
        assertEquals(IntRange(0, 1), wordRange(2.0, 0.0, 8.0, 4, words))
    }

    @Test
    fun `word boundary — null words falls back to single char`() {
        val result = HighlightTracker.findWordRange(4.0, 0.0, 8.0, 8, null)
        assertEquals(IntRange(4, 4), result)
    }

    @Test
    fun `word boundary — empty list falls back to single char`() {
        val result = HighlightTracker.findWordRange(4.0, 0.0, 8.0, 8, emptyList())
        assertEquals(IntRange(4, 4), result)
    }

    @Test
    fun `word boundary — outside window returns null`() {
        val words = listOf(WordBoundary(0, 2), WordBoundary(2, 4))
        val result = HighlightTracker.findWordRange(9.0, 0.0, 8.0, 4, words)
        assertEquals(null, result)
    }

    @Test
    fun `word boundary — first word at window start`() {
        val words = listOf(WordBoundary(0, 3), WordBoundary(3, 6))
        assertEquals(IntRange(0, 2), wordRange(0.0, 0.0, 8.0, 6, words))
    }

    @Test
    fun `word boundary — last word at window end`() {
        val words = listOf(WordBoundary(0, 3), WordBoundary(3, 6))
        assertEquals(IntRange(3, 5), wordRange(8.0, 0.0, 8.0, 6, words))
    }

    @Test
    fun `word boundary — char not in any word falls back`() {
        // Word boundaries don't cover the character → fallback to single char
        val words = listOf(WordBoundary(0, 2), WordBoundary(4, 6))
        // charIndex=3 is between word 0..2 and 4..6
        assertEquals(IntRange(3, 3), wordRange(4.0, 0.0, 8.0, 6, words))
    }

    // ── Timestamp-based word lookup (findWordByTime) ──────────────────────

    @Test
    fun `timestamp — elapsed in first word`() {
        val words = listOf(
            WordBoundary(0, 2, startSec = 0.0, endSec = 0.5),
            WordBoundary(2, 4, startSec = 0.5, endSec = 0.8),
        )
        assertEquals(IntRange(0, 1), HighlightTracker.findWordByTime(0.3, words))
    }

    @Test
    fun `timestamp — elapsed in second word`() {
        val words = listOf(
            WordBoundary(0, 2, startSec = 0.0, endSec = 0.5),
            WordBoundary(2, 4, startSec = 0.5, endSec = 0.8),
        )
        assertEquals(IntRange(2, 3), HighlightTracker.findWordByTime(0.6, words))
    }

    @Test
    fun `timestamp — null words returns null`() {
        assertEquals(null, HighlightTracker.findWordByTime(0.5, null))
    }

    @Test
    fun `timestamp — empty words returns null`() {
        assertEquals(null, HighlightTracker.findWordByTime(0.5, emptyList()))
    }

    @Test
    fun `timestamp — words without timestamps returns null`() {
        val words = listOf(WordBoundary(0, 2), WordBoundary(2, 4))
        assertEquals(null, HighlightTracker.findWordByTime(0.5, words))
    }

    @Test
    fun `timestamp — outside any word returns null`() {
        val words = listOf(
            WordBoundary(0, 2, startSec = 0.0, endSec = 0.5),
        )
        assertEquals(null, HighlightTracker.findWordByTime(0.7, words))
    }

    // ── JSON parsing: word timestamps are extracted ─────────────────────────

    @Test
    fun `parse subtitle JSON extracts per-word timestamps`() {
        // Realistic server payload — words array has startSec/endSec from sherpa-onnx.
        val payload = """
            {"start":824.0,"end":832.0,"text_zh":"你好世界欢迎",
             "words":[
               {"charStart":0,"charEnd":2,"startSec":0.12,"endSec":0.45},
               {"charStart":2,"charEnd":4,"startSec":0.45,"endSec":0.68},
               {"charStart":4,"charEnd":6,"startSec":0.68,"endSec":1.02}
             ]}
        """.trimIndent().replace("\n", "")

        // Parse the same way flush() does.
        val json = org.json.JSONObject(payload)
        val words = if (json.has("words")) {
            val arr = json.getJSONArray("words")
            (0 until arr.length()).map { i ->
                val w = arr.getJSONObject(i)
                WordBoundary(
                    charStart = w.getInt("charStart"),
                    charEnd = w.getInt("charEnd"),
                    startSec = w.optDouble("startSec", 0.0),
                    endSec = w.optDouble("endSec", 0.0),
                )
            }
        } else null

        // Verify word boundaries are correct.
        assertEquals(3, words!!.size)

        // Word 0: "你好" (chars 0-2), timestamps 0.12–0.45.
        assertEquals(0, words[0].charStart)
        assertEquals(2, words[0].charEnd)
        assertEquals(0.12, words[0].startSec, 0.001)
        assertEquals(0.45, words[0].endSec, 0.001)

        // Word 1: "世界" (chars 2-4), timestamps 0.45–0.68.
        assertEquals(2, words[1].charStart)
        assertEquals(4, words[1].charEnd)
        assertEquals(0.45, words[1].startSec, 0.001)
        assertEquals(0.68, words[1].endSec, 0.001)

        // Word 2: "欢迎" (chars 4-6), timestamps 0.68–1.02.
        assertEquals(4, words[2].charStart)
        assertEquals(6, words[2].charEnd)
        assertEquals(0.68, words[2].startSec, 0.001)
        assertEquals(1.02, words[2].endSec, 0.001)

        // Now findWordByTime should work — elapsed=0.3s falls inside word 0.
        val range = HighlightTracker.findWordByTime(0.3, words)
        assertEquals(IntRange(0, 1), range)
    }

    @Test
    fun `parse subtitle JSON without timestamps still works`() {
        // Server may omit startSec/endSec (legacy mode or no sherpa-onnx).
        val payload = """
            {"start":100.0,"end":108.0,"text_zh":"测试",
             "words":[
               {"charStart":0,"charEnd":1},
               {"charStart":1,"charEnd":2}
             ]}
        """.trimIndent().replace("\n", "")

        val json = org.json.JSONObject(payload)
        val words = if (json.has("words")) {
            val arr = json.getJSONArray("words")
            (0 until arr.length()).map { i ->
                val w = arr.getJSONObject(i)
                WordBoundary(
                    charStart = w.getInt("charStart"),
                    charEnd = w.getInt("charEnd"),
                    startSec = w.optDouble("startSec", 0.0),
                    endSec = w.optDouble("endSec", 0.0),
                )
            }
        } else null

        // Words should be parsed but timestamps default to 0.0.
        assertEquals(2, words!!.size)
        assertEquals(0.0, words[0].startSec, 0.0)
        assertEquals(0.0, words[0].endSec, 0.0)

        // findWordByTime should return null (no timestamps available).
        assertEquals(null, HighlightTracker.findWordByTime(0.5, words))

        // But findWordRange should fall back correctly.
        val range = HighlightTracker.findWordRange(4.0, 0.0, 8.0, 2, words)
        assertEquals(IntRange(1, 1), range) // charIndex=1 → word [1,2)
    }
}

package com.crimobile.ui

import com.crimobile.model.WordEntry
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class CharCellTest {

    // ── Regression: char_pinyin absent / wrong-size ──────────────────
    // When per-character pinyin is missing or malformed, buildCharCells
    // must show EMPTY pinyin for every character — NEVER leak the
    // whole-word pinyin onto the first character.

    @Test
    fun `missing char_pinyin — empty syllables for all chars, no word pinyin on first char`() {
        // Two-char word with word-level pinyin but NO char_pinyin.
        // The old fallback (ci==0 → word.pinyin) would put "shìdiǎn" on 试.
        val words = listOf(
            WordEntry(
                text = "试点", char_start = 0, char_end = 2,
                start_sec = 0.0, end_sec = 1.0,
                pinyin = "shìdiǎn", char_pinyin = emptyList(),
                translation = "pilot"
            )
        )
        val cells = buildCharCells(words, showPinyin = true)
        assertEquals("two cells for two chars", 2, cells.size)
        assertEquals("试", cells[0].text)
        assertEquals("点", cells[1].text)
        // CRITICAL: both syllables must be empty — NOT "shìdiǎn" on first char.
        assertEquals("first char syllable empty", "", cells[0].syllable)
        assertEquals("second char syllable empty", "", cells[1].syllable)
    }

    @Test
    fun `char_pinyin wrong size — empty syllables for all chars`() {
        // char_pinyin has 3 entries for a 2-char word (simulates the buggy
        // server path that produced duplicate appends). Must NOT fall back
        // to word pinyin on first char.
        val words = listOf(
            WordEntry(
                text = "试点", char_start = 0, char_end = 2,
                start_sec = 0.0, end_sec = 1.0,
                pinyin = "shìdiǎn",
                char_pinyin = listOf("shì", "shì", "diǎn"), // 3 for 2 chars!
                translation = "pilot"
            )
        )
        val cells = buildCharCells(words, showPinyin = true)
        assertEquals(2, cells.size)
        assertEquals("", cells[0].syllable)
        assertEquals("", cells[1].syllable)
    }

    @Test
    fun `char_pinyin correct — syllables aligned per character`() {
        // Happy path: char_pinyin has exactly one entry per character.
        val words = listOf(
            WordEntry(
                text = "试点", char_start = 0, char_end = 2,
                start_sec = 0.0, end_sec = 1.0,
                pinyin = "shìdiǎn",
                char_pinyin = listOf("shì", "diǎn"),
                translation = "pilot"
            )
        )
        val cells = buildCharCells(words, showPinyin = true)
        assertEquals(2, cells.size)
        assertEquals("试", cells[0].text)
        assertEquals("点", cells[1].text)
        assertTrue("first char has pinyin", cells[0].syllable.isNotEmpty())
        assertTrue("second char has pinyin", cells[1].syllable.isNotEmpty())
        // Pinyin should match the char readings, not be the whole word.
        assertEquals("shì", cells[0].syllable)
        assertEquals("diǎn", cells[1].syllable)
    }

    @Test
    fun `single char word without char_pinyin — space-split fallback works correctly`() {
        // For single-char words, word.pinyin IS the char pinyin.
        // The space-split fallback (syllables.size == chars.size) should work.
        val words = listOf(
            WordEntry(
                text = "的", char_start = 0, char_end = 1,
                start_sec = 0.0, end_sec = 0.5,
                pinyin = "de", char_pinyin = emptyList(),
                translation = "of"
            )
        )
        val cells = buildCharCells(words, showPinyin = true)
        assertEquals(1, cells.size)
        assertEquals("的", cells[0].text)
        assertEquals("de", cells[0].syllable) // single char → word pinyin is char pinyin
    }

    @Test
    fun `multi char word without char_pinyin — space-split works when syllables match chars`() {
        // Word pinyin "shì diǎn" has 2 space-separated tokens for 2 chars.
        val words = listOf(
            WordEntry(
                text = "试点", char_start = 0, char_end = 2,
                start_sec = 0.0, end_sec = 1.0,
                pinyin = "shì diǎn", char_pinyin = emptyList(),
                translation = "pilot"
            )
        )
        val cells = buildCharCells(words, showPinyin = true)
        assertEquals(2, cells.size)
        assertEquals("shì", cells[0].syllable)
        assertEquals("diǎn", cells[1].syllable)
    }

    @Test
    fun `punctuation is separate zero-width cell`() {
        val words = listOf(
            WordEntry(text="开始", char_start=0, char_end=2, start_sec=0.0, end_sec=1.0, pinyin="kai1 shi3", translation=""),
            WordEntry(text="。", char_start=2, char_end=3, start_sec=1.0, end_sec=2.0, pinyin="。", translation=""),
        )
        val cells = buildCharCells(words, showPinyin = false)
        // "开" "始" "。" — punctuation is separate cell
        assertEquals(3, cells.size)
        assertEquals("开", cells[0].text)
        assertEquals("始", cells[1].text)
        assertEquals("。", cells[2].text)
        assertEquals("", cells[2].syllable) // no pinyin for punct
    }

    @Test
    fun `punctuation has empty syllable`() {
        val words = listOf(
            WordEntry(text="江南北部", char_start=0, char_end=4, start_sec=0.0, end_sec=2.0, pinyin="jiang1 nan2 bei3 bu4", translation=""),
            WordEntry(text="、", char_start=4, char_end=5, start_sec=2.0, end_sec=2.5, pinyin="、", translation=""),
        )
        val cells = buildCharCells(words, showPinyin = true)
        // All chars + punct separate
        // "江", "南", "北", "部", "、"
        assertEquals(5, cells.size)
        assertTrue(cells[0].syllable.isNotEmpty()) // 江 has pinyin
        assertTrue(cells[4].syllable.isEmpty())    // 、 has no pinyin
    }

    @Test
    fun `punctuation at start of first word stays alone`() {
        val words = listOf(
            WordEntry(text="。", char_start=0, char_end=1, start_sec=0.0, end_sec=0.5, pinyin="。", translation=""),
            WordEntry(text="开始", char_start=1, char_end=3, start_sec=0.5, end_sec=1.5, pinyin="kai1 shi3", translation=""),
        )
        val cells = buildCharCells(words, showPinyin = false)
        assertEquals(3, cells.size)
        assertEquals("。", cells[0].text)
        assertEquals("开", cells[1].text)
        assertEquals("始", cells[2].text)
    }

    @Test
    fun `no punctuation — cells match char count`() {
        val words = listOf(
            WordEntry(text="开始江南", char_start=0, char_end=4, start_sec=0.0, end_sec=2.0, pinyin="kai1 shi3 jiang1 nan2", translation=""),
        )
        val cells = buildCharCells(words, showPinyin = false)
        assertEquals(4, cells.size)
        assertEquals("开", cells[0].text)
        assertEquals("始", cells[1].text)
        assertEquals("江", cells[2].text)
        assertEquals("南", cells[3].text)
    }

    @Test
    fun `isCJKPunctuation recognizes all expected chars`() {
        val puncts = "，。！？；：、"
        for (c in puncts) {
            assertTrue("'$c' should be CJK punctuation", isCJKPunctuation(c))
        }
    }

    @Test
    fun `isCJKPunctuation rejects CJK letters and latin`() {
        assertTrue(!isCJKPunctuation('开'))
        assertTrue(!isCJKPunctuation('a'))
        assertTrue(!isCJKPunctuation('1'))
    }

    @Test
    fun `isCJKPunctuation accepts CJK quotes`() {
        assertTrue(isCJKPunctuation('\"'))
        assertTrue(isCJKPunctuation('\''))
    }

    @Test
    fun `multiple punctuation marks are all separate`() {
        val words = listOf(
            WordEntry(text="行", char_start=0, char_end=1, start_sec=0.0, end_sec=0.3, pinyin="xing2", translation=""),
            WordEntry(text="。", char_start=1, char_end=2, start_sec=0.3, end_sec=0.6, pinyin="。", translation=""),
            WordEntry(text="，", char_start=2, char_end=3, start_sec=0.6, end_sec=0.9, pinyin="，", translation=""),
        )
        val cells = buildCharCells(words, showPinyin = false)
        // "行" "。" "，" — all separate
        assertEquals(3, cells.size)
        assertEquals("行", cells[0].text)
        assertEquals("。", cells[1].text)
        assertEquals("，", cells[2].text)
    }
}

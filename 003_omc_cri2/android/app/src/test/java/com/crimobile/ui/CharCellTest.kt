package com.crimobile.ui

import com.crimobile.model.WordEntry
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class CharCellTest {

    @Test
    fun `punctuation is separate zero-width cell`() {
        val words = listOf(
            WordEntry("开始", 0, 2, 0.0, 1.0, "kai1 shi3", ""),
            WordEntry("。", 2, 3, 1.0, 2.0, "。", ""),
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
            WordEntry("江南北部", 0, 4, 0.0, 2.0, "jiang1 nan2 bei3 bu4", ""),
            WordEntry("、", 4, 5, 2.0, 2.5, "、", ""),
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
            WordEntry("。", 0, 1, 0.0, 0.5, "。", ""),
            WordEntry("开始", 1, 3, 0.5, 1.5, "kai1 shi3", ""),
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
            WordEntry("开始江南", 0, 4, 0.0, 2.0, "kai1 shi3 jiang1 nan2", ""),
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
            WordEntry("行", 0, 1, 0.0, 0.3, "xing2", ""),
            WordEntry("。", 1, 2, 0.3, 0.6, "。", ""),
            WordEntry("，", 2, 3, 0.6, 0.9, "，", ""),
        )
        val cells = buildCharCells(words, showPinyin = false)
        // "行" "。" "，" — all separate
        assertEquals(3, cells.size)
        assertEquals("行", cells[0].text)
        assertEquals("。", cells[1].text)
        assertEquals("，", cells[2].text)
    }
}

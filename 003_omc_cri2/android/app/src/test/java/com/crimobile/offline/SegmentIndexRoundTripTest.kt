package com.crimobile.offline

import com.crimobile.model.SubtitleSegment
import com.crimobile.model.WordEntry
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

/**
 * Verifies the lightweight segment index round-trips the [com.crimobile.model.SegmentMeta]
 * fields used for timeline navigation (the index intentionally drops word-level data).
 */
class SegmentIndexRoundTripTest {

    @get:Rule
    val tmp = TemporaryFolder()

    @Test
    fun `write then read preserves meta fields`() {
        val dir = tmp.newFolder("meta")
        val segs = listOf(
            SubtitleSegment(
                segment_id = 2, timeline_start_sec = 103.0, timeline_end_sec = 106.0,
                ts_file = "2.ts", text_zh = "b", text_pinyin = "p", text_en = "e",
                words = listOf(WordEntry(
                    text = "b", char_start = 0, char_end = 1,
                    start_sec = 0.0, end_sec = 1.0, pinyin = "b", translation = "b"
                ))
            ),
            SubtitleSegment(
                segment_id = 1, timeline_start_sec = 100.0, timeline_end_sec = 103.0,
                ts_file = "1.ts", text_zh = "a", text_pinyin = "p", text_en = "e",
                words = emptyList()
            )
        )

        SegmentIndex.write(dir, segs)
        val metas = SegmentIndex.read(dir)

        assertEquals(2, metas.size)
        // Written in input order; read preserves that order.
        assertEquals(2, metas[0].segment_id)
        assertEquals(103.0, metas[0].timeline_start_sec, 0.001)
        assertEquals(106.0, metas[0].timeline_end_sec, 0.001)
        assertEquals("b", metas[0].text_zh)
        assertEquals(1, metas[1].segment_id)
    }

    @Test
    fun `read returns empty list when index is missing`() {
        val dir = tmp.newFolder("empty")
        assertTrue(SegmentIndex.read(dir).isEmpty())
    }
}

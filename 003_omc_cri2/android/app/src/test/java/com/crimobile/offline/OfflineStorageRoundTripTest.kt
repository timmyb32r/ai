package com.crimobile.offline

import com.crimobile.model.SubtitleSegment
import com.crimobile.model.WordEntry
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

/**
 * Regression test for the offline subtitle data drift.
 *
 * Previously [OfflineStorageManager.saveSegment] used a private serializer that
 * dropped `char_pinyin_uncertain`, `cedict_meanings`, and `wiktionary_meanings`,
 * so the offline word popup lost CEDICT/Wiktionary glosses and probabilistic-fill
 * flags. saveSegment now uses the canonical [com.crimobile.subtitles.SubtitleParser.segmentToJson]
 * and this test exercises the real save→load path.
 */
class OfflineStorageRoundTripTest {

    @get:Rule
    val tmp = TemporaryFolder()

    private fun newStore(): OfflineStorageManager =
        OfflineStorageManager.forRoot(tmp.newFolder("cri_offline"))

    private fun sampleWord() = WordEntry(
        text = "试点", char_start = 0, char_end = 2,
        start_sec = 1.0, end_sec = 2.0,
        pinyin = "shìdiǎn", translation = "pilot",
        char_pinyin = listOf("shì", "diǎn"),
        char_pinyin_uncertain = listOf(true, false),
        cedict_meanings = listOf("to pilot", "pilot zone"),
        wiktionary_meanings = listOf("pilot (experiment)")
    )

    private fun sampleSegment() = SubtitleSegment(
        segment_id = 42,
        timeline_start_sec = 100.0,
        timeline_end_sec = 103.0,
        ts_file = "000000042.ts",
        text_zh = "试点", text_pinyin = "shì diǎn", text_en = "pilot",
        words = listOf(sampleWord())
    )

    @Test
    fun `saveSegment preserves cedict, wiktionary and uncertainty fields`() {
        val store = newStore()
        val sid = store.createSession(1000L, 60)
        val seg = sampleSegment()

        store.saveSegment(seg, ByteArray(188) { 0x47 }, sid)
        val loaded = store.loadFullSegment(sid, 42)

        assertNotNull(loaded)
        val w = loaded!!.words[0]
        assertEquals(listOf(true, false), w.char_pinyin_uncertain)
        assertEquals(listOf("to pilot", "pilot zone"), w.cedict_meanings)
        assertEquals(listOf("pilot (experiment)"), w.wiktionary_meanings)
        assertEquals(listOf("shì", "diǎn"), w.char_pinyin)
        assertEquals("shìdiǎn", w.pinyin)
        assertEquals(42, loaded.segment_id)
    }

    @Test
    fun `countSegmentsInSession excludes index and temp files`() {
        val store = newStore()
        val sid = store.createSession(2000L, 60)
        store.saveSegment(sampleSegment(), ByteArray(188) { 0x47 }, sid)
        // Force-build the segment index so an index file sits next to the meta file.
        store.writeSegmentIndex(sid, listOf(sampleSegment()))

        // One real segment file; the index file must NOT be counted.
        assertEquals(1, store.countSegmentsInSession(sid))
    }

    @Test
    fun `computeLocalRange returns actual min start and max end, not first_last`() {
        val store = newStore()
        val startSec = 1000L
        val sid = store.createSession(startSec, 60)
        // seg1 has a LATER timeline than seg2 — saved/indexed in that order so
        // first()/last() would return the wrong range; min/max must be used.
        val seg1 = SubtitleSegment(
            segment_id = 1, timeline_start_sec = 200.0, timeline_end_sec = 203.0,
            ts_file = "1.ts", text_zh = "a", text_pinyin = "p", text_en = "e",
            words = listOf(WordEntry(
                text = "a", char_start = 0, char_end = 1,
                start_sec = 0.0, end_sec = 1.0, pinyin = "a", translation = "a"
            ))
        )
        val seg2 = SubtitleSegment(
            segment_id = 2, timeline_start_sec = 103.0, timeline_end_sec = 106.0,
            ts_file = "2.ts", text_zh = "b", text_pinyin = "p", text_en = "e",
            words = emptyList()
        )
        store.saveSegment(seg1, ByteArray(188) { 0x47 }, sid)
        store.saveSegment(seg2, ByteArray(188) { 0x47 }, sid)
        store.writeSegmentIndex(sid, listOf(seg1, seg2))
        store.writeSessionsIndex(listOf(OfflineStorageManager.SessionMeta(startSec, 60, 2, 0L)))

        val range = store.computeLocalRange()
        assertNotNull(range)
        assertEquals(103.0, range!!.first, 0.001)  // min start = seg2
        assertEquals(203.0, range.second, 0.001)   // max end = seg1
    }
}

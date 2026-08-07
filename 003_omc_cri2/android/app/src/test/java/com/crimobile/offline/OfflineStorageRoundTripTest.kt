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
}

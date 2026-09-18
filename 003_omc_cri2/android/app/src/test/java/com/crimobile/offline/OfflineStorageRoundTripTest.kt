package com.crimobile.offline

import com.crimobile.model.SubtitleSegment
import com.crimobile.model.WordEntry
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File
import java.io.IOException
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

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

    @Test
    fun `rebuilding continuous audio never appends old continuous file`() {
        val store = newStore()
        val sid = store.createSession(1000, 60)
        store.saveSegment(sampleSegment(), byteArrayOf(1, 2, 3), sid)
        assertArrayEquals(byteArrayOf(1, 2, 3), store.concatAudioFiles(sid)!!.readBytes())
        assertArrayEquals(byteArrayOf(1, 2, 3), store.concatAudioFiles(sid)!!.readBytes())
    }

    @Test
    fun `discarding incomplete staging does not alter saved session`() {
        val store = newStore()
        val sid = store.createSession(1000, 60)
        store.saveSegment(sampleSegment(), byteArrayOf(1, 2, 3), sid)
        val staging = store.createDownloadSession()
        store.saveSegment(sampleSegment(), byteArrayOf(9), staging)
        store.discardDownload(staging)
        assertArrayEquals(byteArrayOf(1, 2, 3), store.getAudioFile(sid, 42)!!.readBytes())
        assertFalse(store.sessionDir(staging).exists())
    }

    @Test
    fun `index write failure rolls back replacement and preserves the saved index`() {
        val store = newStore()
        val sid = store.createSession(1000, 60)
        store.saveSegment(sampleSegment(), byteArrayOf(1, 2, 3), sid)
        val previous = OfflineStorageManager.SessionMeta(1000, 60, 1, 1234)
        store.writeSessionsIndex(listOf(previous))
        val sessionsDir = store.sessionDir(sid).parentFile!!
        val index = File(sessionsDir, "index.json")
        val previousIndex = index.readBytes()
        val staging = store.createDownloadSession()
        store.saveSegment(sampleSegment(), byteArrayOf(9), staging)

        // A real filesystem failure after the directory swap, without mocking
        // storage methods: the index temp path cannot be opened as a file.
        val blockedTemp = File(sessionsDir, ".index.json.tmp")
        assertTrue(blockedTemp.mkdir())
        File(blockedTemp, "blocker").writeText("keep directory nonempty")
        try {
            store.publishDownload(staging, 1000, 60, 1)
            fail("Expected index write to fail")
        } catch (_: IOException) {
            // Expected; the old session and its exact index must survive.
        }

        assertArrayEquals(previousIndex, index.readBytes())
        assertEquals(listOf(previous), store.loadAllSessions())
        assertArrayEquals(byteArrayOf(1, 2, 3), store.getAudioFile(sid, 42)!!.readBytes())
        assertArrayEquals(byteArrayOf(9), store.getAudioFile(staging, 42)!!.readBytes())
        assertFalse(sessionsDir.listFiles()!!.any { it.name.startsWith(".replaced-") })
        store.discardDownload(staging)
        assertArrayEquals(byteArrayOf(1, 2, 3), store.getAudioFile(sid, 42)!!.readBytes())
    }

    @Test
    fun `successful replacement commits audio and index together`() {
        val store = newStore()
        val sid = store.createSession(1000, 60)
        store.saveSegment(sampleSegment(), byteArrayOf(1), sid)
        store.writeSessionsIndex(listOf(OfflineStorageManager.SessionMeta(1000, 60, 1, 1234)))
        val staging = store.createDownloadSession()
        store.saveSegment(sampleSegment(), byteArrayOf(8), staging)
        store.saveSegment(sampleSegment().copy(segment_id = 43, ts_file = "000000043.ts"), byteArrayOf(9), staging)

        store.publishDownload(staging, 1000, 60, 2)

        assertArrayEquals(byteArrayOf(8), store.getAudioFile(sid, 42)!!.readBytes())
        assertArrayEquals(byteArrayOf(9), store.getAudioFile(sid, 43)!!.readBytes())
        assertEquals(2, store.loadAllSessions().single().segmentCount)
        assertFalse(store.sessionDir(staging).exists())
        assertFalse(store.sessionDir(sid).parentFile!!.listFiles()!!.any { it.name.startsWith(".replaced-") })
    }

    @Test
    fun `concurrent publishers from separate managers retain every session`() {
        val root = tmp.newFolder("shared_offline")
        val stores = List(2) { OfflineStorageManager.forRoot(root) }
        val pool = Executors.newFixedThreadPool(2)
        try {
            repeat(4) { round ->
                val ready = CountDownLatch(2)
                val start = CountDownLatch(1)
                val publications = stores.mapIndexed { index, store ->
                    val startSec = 1000L + round * 120 + index * 60
                    val staging = store.createDownloadSession()
                    store.saveSegment(sampleSegment(), byteArrayOf((round * 2 + index).toByte()), staging)
                    pool.submit {
                        ready.countDown()
                        check(start.await(5, TimeUnit.SECONDS))
                        store.publishDownload(staging, startSec, 60, 1)
                    }
                }
                assertTrue(ready.await(5, TimeUnit.SECONDS))
                start.countDown()
                publications.forEach { it.get(5, TimeUnit.SECONDS) }
            }
            val sessions = stores.first().loadAllSessions()
            assertEquals((0..7).map { 1000L + it * 60 }.toSet(), sessions.map { it.startSec }.toSet())
            assertEquals(8, sessions.size)
            sessions.forEach { session ->
                assertEquals(1, session.segmentCount)
                val sid = stores.first().sessionId(session.startSec, session.durationSec)
                assertArrayEquals(byteArrayOf(((session.startSec - 1000) / 60).toByte()), stores.first().getAudioFile(sid, 42)!!.readBytes())
            }
        } finally {
            pool.shutdownNow()
            assertTrue(pool.awaitTermination(5, TimeUnit.SECONDS))
        }
    }

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

package com.crimobile.offline

import com.crimobile.model.SubtitleSegment
import com.crimobile.model.WordEntry
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder

/**
 * Regression tests for the segment cache main-thread safety.
 *
 * Previously the UI called [SegmentCache.getOrLoad] synchronously during
 * composition, so every visible cache-miss after a session switch blocked the
 * main thread (disk read + JSON parse). [getIfCached] (no I/O) and
 * [getOrLoadAsync] (background dispatcher) now let composition stay on the
 * main thread.
 */
class SegmentCacheTest {

    @get:Rule
    val tmp = TemporaryFolder()

    private fun newStore() = OfflineStorageManager.forRoot(tmp.newFolder("store"))

    private fun seg(id: Int) = SubtitleSegment(
        segment_id = id,
        timeline_start_sec = id * 3.0,
        timeline_end_sec = id * 3.0 + 3.0,
        ts_file = "$id.ts", text_zh = "zh$id", text_pinyin = "py$id", text_en = "en$id",
        words = listOf(WordEntry(
            text = "w$id", char_start = 0, char_end = 1,
            start_sec = 0.0, end_sec = 1.0, pinyin = "p", translation = "t"
        ))
    )

    @Test
    fun `getIfCached returns null on miss without loading`() = runTest {
        val store = newStore()
        val sid = store.createSession(1000L, 60)
        store.saveSegment(seg(42), ByteArray(188) { 0x47 }, sid)
        val cache = SegmentCache(store, sid, maxSize = 5)

        // Not loaded yet → null, and crucially NO disk I/O (safe on main thread).
        assertNull(cache.getIfCached(42))
        assertEquals(0, cache.size())
    }

    @Test
    fun `getOrLoadAsync loads a miss into the cache`() = runTest {
        val store = newStore()
        val sid = store.createSession(1000L, 60)
        store.saveSegment(seg(42), ByteArray(188) { 0x47 }, sid)
        val cache = SegmentCache(store, sid, maxSize = 5)

        val loaded = cache.getOrLoadAsync(42)
        assertNotNull(loaded)
        assertEquals(42, loaded!!.segment_id)

        // Now in memory — getIfCached returns the same instance without I/O.
        assertSame(loaded, cache.getIfCached(42))
        assertEquals(1, cache.size())
    }

    @Test
    fun `getOrLoadAsync returns null when the segment file is absent`() = runTest {
        val store = newStore()
        val sid = store.createSession(1000L, 60)
        val cache = SegmentCache(store, sid, maxSize = 5)

        assertNull(cache.getOrLoadAsync(999))
        assertEquals(0, cache.size())
    }

    @Test
    fun `LRU evicts oldest non-pinned segment but keeps the pinned one`() = runTest {
        val store = newStore()
        val sid = store.createSession(1000L, 60)
        for (id in 1..6) store.saveSegment(seg(id), ByteArray(188) { 0x47 }, sid)
        val cache = SegmentCache(store, sid, maxSize = 3)

        cache.getOrLoadAsync(1)
        cache.getOrLoadAsync(2)
        cache.getOrLoadAsync(3)
        cache.pin(2) // segment 2 must survive eviction

        // Overflow the cache — pinned segment 2 stays.
        cache.getOrLoadAsync(4)
        cache.getOrLoadAsync(5)
        cache.getOrLoadAsync(6)

        assertNotNull("pinned segment survives eviction", cache.getIfCached(2))
        assertNull("oldest non-pinned segment evicted", cache.getIfCached(1))
        assertNull("next non-pinned segment evicted", cache.getIfCached(3))
        assertNotNull("newest segment present", cache.getIfCached(6))
        // Pinned items are never evicted, so the cache may hold up to maxSize + 1.
        assertTrue("cache size within bound+1 (was ${cache.size()})", cache.size() <= 4)
    }
}

package com.crimobile.offline

import com.crimobile.model.SubtitleSegment

/**
 * LRU cache of fully-loaded [SubtitleSegment] objects.
 *
 * Keeps at most [maxSize] segments in memory.  The "active" segment
 * (currently playing) can be [pin]ned — it is never evicted.
 *
 * Public methods are [Synchronized] so the cache is safe to call
 * from both the UI thread (preload) and the sync loop (getOrLoad).
 *
 * @param storageManager  used to load a single segment from disk on cache miss
 * @param sessionId       the session whose per-segment JSON files to read
 * @param maxSize         maximum number of cached full segments (default 15)
 */
class SegmentCache(
    private val storageManager: OfflineStorageManager,
    private val sessionId: String,
    private val maxSize: Int = 15
) {
    private val cache = object : LinkedHashMap<Int, SubtitleSegment>(maxSize, 0.75f, /* accessOrder = */ true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<Int, SubtitleSegment>): Boolean {
            if (eldest.key == pinnedSegmentId) return false
            return size > maxSize
        }
    }

    private var pinnedSegmentId: Int? = null

    /** Pin [segmentId] so it is never evicted, and ensure it is loaded. */
    @Synchronized
    fun pin(segmentId: Int) {
        pinnedSegmentId = segmentId
        getOrLoad(segmentId)
    }

    /** Release the pinned segment. */
    @Synchronized
    fun unpin() {
        pinnedSegmentId = null
    }

    /** Return the cached segment, loading it from disk if necessary. */
    @Synchronized
    fun getOrLoad(segmentId: Int): SubtitleSegment? {
        cache[segmentId]?.let { return it }
        val seg = storageManager.loadFullSegment(sessionId, segmentId) ?: return null
        cache[segmentId] = seg
        return seg
    }

    /** Pre-load all given segment IDs — best-effort, failures are silent. */
    @Synchronized
    fun preloadVisible(visibleIds: Set<Int>) {
        for (id in visibleIds) {
            if (id !in cache) {
                getOrLoad(id)
            }
        }
    }

    /** Current number of cached segments. */
    @Synchronized
    fun size(): Int = cache.size

    /** Clear all cached segments (e.g. on session switch). */
    @Synchronized
    fun clear() {
        pinnedSegmentId = null
        cache.clear()
    }
}

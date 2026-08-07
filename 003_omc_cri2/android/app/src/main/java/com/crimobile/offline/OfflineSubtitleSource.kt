package com.crimobile.offline

import com.crimobile.model.ConnectionStatus
import com.crimobile.model.SegmentMeta
import com.crimobile.model.SubtitleSegment
import com.crimobile.subtitles.SubtitleSource
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/**
 * Provides [SubtitleSource] from locally stored segments.
 *
 * [connect] and [disconnect] are no-ops — all data comes from disk.
 * Call [load] when switching to offline mode to populate segments.
 */
class OfflineSubtitleSource(
    private val storageManager: OfflineStorageManager
) : SubtitleSource {

    private val _segments = MutableStateFlow<List<SubtitleSegment>>(emptyList())
    override val segments: StateFlow<List<SubtitleSegment>> = _segments.asStateFlow()

    private val _connected = MutableStateFlow(ConnectionStatus.DISCONNECTED)
    override val connected: StateFlow<ConnectionStatus> = _connected.asStateFlow()

    private val _segmentsMeta = MutableStateFlow<List<SegmentMeta>>(emptyList())
    override val segmentsMeta: StateFlow<List<SegmentMeta>> = _segmentsMeta.asStateFlow()

    /** Lazily-created LRU cache of full segment data. Created in [load]. */
    var segmentCache: SegmentCache? = null
        private set

    /** ID of the session currently loaded (or last loaded). */
    var lastLoadedSessionId: String? = null
        private set

    // Guards the load/disconnect path so a concurrent switchPlaybackMode and
    // startDownload cannot interleave and clobber segmentCache / flows.
    private val lock = Any()

    /** Load segments from the most recent session. Call on main thread. */
    fun load() {
        synchronized(lock) {
            val latestSession = storageManager.loadAllSessions().maxByOrNull { it.createdAt }
            val sessionId = latestSession?.let {
                storageManager.sessionId(it.startSec, it.durationSec)
            }
            val meta = if (sessionId != null) {
                storageManager.loadSegmentsForSession(sessionId)
            } else emptyList()
            lastLoadedSessionId = sessionId
            _segmentsMeta.value = meta
            segmentCache = if (sessionId != null) SegmentCache(storageManager, sessionId) else null
            _segments.value = emptyList()
            _connected.value = if (meta.isNotEmpty()) ConnectionStatus.CONNECTED
            else ConnectionStatus.DISCONNECTED
        }
    }

    /** Load a single full segment on demand (e.g. when user taps a timeline position). */
    fun loadFullSegmentAsync(segmentId: Int): SubtitleSegment? = segmentCache?.getOrLoad(segmentId)

    /** Pre-warm the cache for segments that are about to become visible. */
    fun preloadVisible(visibleIds: Set<Int>) {
        segmentCache?.preloadVisible(visibleIds)
    }

    override fun connect(serverUrl: String) {
        // no-op: offline source reads from disk
    }

    override fun disconnect() {
        synchronized(lock) {
            segmentCache?.clear()
            segmentCache = null
            _segments.value = emptyList()
            _segmentsMeta.value = emptyList()
            _connected.value = ConnectionStatus.DISCONNECTED
        }
    }
}

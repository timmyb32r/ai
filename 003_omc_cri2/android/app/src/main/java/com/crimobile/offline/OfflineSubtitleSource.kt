package com.crimobile.offline

import com.crimobile.model.ConnectionStatus
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

    /** Load all stored segments from disk. Call on main thread. */
    fun load() {
        val all = storageManager.loadAllSegments()
        _segments.value = all
        _connected.value = if (all.isNotEmpty()) ConnectionStatus.CONNECTED
        else ConnectionStatus.DISCONNECTED
    }

    override fun connect(serverUrl: String) {
        // no-op: offline source reads from disk
    }

    override fun disconnect() {
        // no-op: no server connection to tear down
    }
}

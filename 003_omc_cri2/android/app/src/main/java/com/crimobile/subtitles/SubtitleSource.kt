package com.crimobile.subtitles

import com.crimobile.model.ConnectionStatus
import com.crimobile.model.SubtitleSegment
import kotlinx.coroutines.flow.StateFlow

/**
 * Receives subtitle segments from the server via SSE.
 * Maintains a local cache ordered by timeline position.
 */
interface SubtitleSource {
    /** All received segments, sorted by timeline_start_sec. */
    val segments: StateFlow<List<SubtitleSegment>>

    /** Connection status. */
    val connected: StateFlow<ConnectionStatus>

    /** Connect to the server's SSE endpoint. */
    fun connect(serverUrl: String)

    /** Disconnect and clear cached segments. */
    fun disconnect()
}

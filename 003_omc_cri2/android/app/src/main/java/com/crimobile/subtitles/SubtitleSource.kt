package com.crimobile.subtitles

import com.crimobile.model.ConnectionStatus
import com.crimobile.model.SegmentMeta
import com.crimobile.model.SubtitleSegment
import kotlinx.coroutines.flow.StateFlow

/**
 * Receives subtitle segments from the server via SSE.
 * Maintains a local cache ordered by timeline position.
 */
interface SubtitleSource {
    /** All received segments, sorted by timeline_start_sec. */
    val segments: StateFlow<List<SubtitleSegment>>

    /** Lightweight segment metadata for timeline navigation, always kept in RAM. */
    val segmentsMeta: StateFlow<List<SegmentMeta>>

    /** Connection status. */
    val connected: StateFlow<ConnectionStatus>

    /** Connect to the server's SSE endpoint. */
    fun connect(serverUrl: String)

    /** Disconnect and clear cached segments. */
    fun disconnect()

    /**
     * Fetch a single segment with full word-level dictionary data.
     * Returns null if the source doesn't support on-demand fetching
     * (SSE always sends full data; offline reads from disk).
     */
    suspend fun fetchSegmentFull(serverUrl: String, segmentId: Int): SubtitleSegment? = null

    /**
     * Insert or replace a segment in the local cache and re-emit.
     * Used after [fetchSegmentFull] to persist the full-data segment
     * so subsequent taps on the same segment don't re-fetch.
     */
    fun upsertSegment(segment: SubtitleSegment) {}
}

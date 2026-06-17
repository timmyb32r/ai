package com.crimobile.api

import com.crimobile.model.SubtitleEvent
import kotlinx.coroutines.flow.StateFlow

/**
 * SSE-based source of timestamped subtitle events.
 *
 * The implementation connects to the server's `/v1/stream/subtitles` endpoint,
 * parses the SSE stream, and accumulates subtitle events.  Callers periodically
 * drain the accumulated events via [drainPending].
 *
 * A "jump" event signals that the subscriber fell behind and must reconnect.
 */
interface SubtitleStreamSource {
    /** Open the SSE connection to [baseUrl].  May be called again after [disconnect]. */
    fun connect(baseUrl: String)

    /** Close the SSE connection.  Idempotent. */
    fun disconnect()

    /**
     * Atomically returns all subtitle events accumulated since the last call
     * and clears the internal buffer.
     *
     * Thread-safe — may be called from any thread.
     */
    fun drainPending(): List<SubtitleEvent>

    /**
     * True if the server signaled a jump (subscriber fell behind).
     * The caller should tear down and reconnect.
     */
    val hasJumpEvent: Boolean

    /**
     * The audio timeline start from the SSE `sync` event (seconds since server
     * ingest start).  Null until the first sync event arrives.  Thread-safe
     * via [StateFlow].
     */
    val audioTimelineStartSec: StateFlow<Double?>
}

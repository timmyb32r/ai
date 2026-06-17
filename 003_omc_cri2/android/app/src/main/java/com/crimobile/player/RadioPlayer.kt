package com.crimobile.player

import com.crimobile.model.PlaybackState
import kotlinx.coroutines.flow.StateFlow

/**
 * Wraps Media3 ExoPlayer for HLS radio playback.
 * Timeline uses Unix epoch milliseconds — correlated with
 * server-side #EXT-X-PROGRAM-DATE-TIME via window.windowStartTimeMs.
 */
interface RadioPlayer {
    /** Current playback position as Unix epoch milliseconds. */
    val currentTimelineMs: StateFlow<Long>

    /** Current player state. */
    val playbackState: StateFlow<PlaybackState>

    /** True when playback has fallen behind the live DVR window. */
    val behindLiveWindow: StateFlow<Boolean>

    /** Start playing the HLS stream at the given URL. */
    fun play(hlsUrl: String)

    /** Pause playback, preserving position for resume. */
    fun pause()

    /** Resume from paused position. */
    fun resume()

    /** Seek to the given Unix epoch millisecond position. */
    fun seekTo(timelineMs: Long)

    /** Seek to the live edge. */
    fun seekToLiveEdge()

    /** Release player resources. */
    fun release()
}

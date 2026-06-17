package com.crimobile.api

import com.crimobile.model.PlaybackState
import com.crimobile.model.SubtitleEvent
import com.crimobile.model.WordBoundary
import kotlinx.coroutines.flow.StateFlow

/**
 * Central coordinator for audio+text synchronised playback.
 *
 * Replaces the monolithic PlaybackEngine.  Internally delegates to focused
 * services ([SubtitleStreamSource], [HighlightTracker], [androidx.media3.exoplayer.ExoPlayer])
 * while exposing a simple public API
 * for state transitions and word interactions.
 */
interface PlaybackStateMachine {
    // ── Observables ──────────────────────────────────────────────────

    val state: StateFlow<PlaybackState>
    val segments: StateFlow<List<com.crimobile.model.SyncedSegment>>
    val highlightRange: StateFlow<IntRange?>
    val debugLog: StateFlow<List<String>>
    val audioFlowing: StateFlow<Boolean>

    // ── State transitions ────────────────────────────────────────────

    /** IDLE/PAUSED → LOADING.  Opens connections. */
    fun play()

    /** PLAYING → PAUSED.  Stops audio, KEEPS text. */
    fun pause()

    /** PAUSED → PLAYING.  Resumes audio. */
    fun resume()

    /** Any → IDLE.  Full teardown. */
    fun stop()

    /** Toggle PLAYING ↔ PAUSED, or start from IDLE/PAUSED. */
    fun togglePlayPause()

    // ── Word interactions ────────────────────────────────────────────

    /** Pause and seek to a word within a subtitle. */
    fun onWordClick(wb: WordBoundary, subtitle: SubtitleEvent)

    /** Play pronunciation of a word from the PCM cache. */
    fun pronounceWord(wb: WordBoundary, subtitle: SubtitleEvent)

    // ── Seek ─────────────────────────────────────────────────────────

    fun seekRelative(deltaSec: Double)

    // ── Logging ──────────────────────────────────────────────────────

    fun clearDebugLog()
}

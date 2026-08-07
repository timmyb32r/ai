package com.crimobile.player

import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.MediaMetadata
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.common.Timeline
import androidx.media3.exoplayer.ExoPlayer
import com.crimobile.model.PlaybackState
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import com.crimobile.debug.DebugLogger

private const val TAG = "CRIRadio:player"

class ExoRadioPlayer(
    private val player: ExoPlayer  // injected — PlayerService owns the ExoPlayer for MediaSession
) : RadioPlayer {

    private val scope = CoroutineScope(Dispatchers.Main)

    private val _currentTimelineMs = MutableStateFlow(0L)
    override val currentTimelineMs: StateFlow<Long> = _currentTimelineMs.asStateFlow()

    private val _playbackState = MutableStateFlow(PlaybackState.IDLE)
    override val playbackState: StateFlow<PlaybackState> = _playbackState.asStateFlow()

    private val _behindLiveWindow = MutableStateFlow(false)
    override val behindLiveWindow: StateFlow<Boolean> = _behindLiveWindow.asStateFlow()

    private val _lastErrorMessage = MutableStateFlow<String?>(null)
    override val lastErrorMessage: StateFlow<String?> = _lastErrorMessage.asStateFlow()

    private var pausedAtTimelineMs: Long = 0L
    private var currentHlsUrl: String? = null
    private val retry = RetryController(MAX_RETRIES, RETRY_BASE_DELAY_MS, RETRY_MAX_DELAY_MS)
    private var retryJob: Job? = null

    // Stored as a field so release() can remove it (defensive — ExoPlayer.release
    // also clears listeners, but explicit removal is correct hygiene and safe if
    // the player is ever reused).
    private val listener = object : Player.Listener {
        override fun onPlaybackStateChanged(state: Int) {
            val newState = when (state) {
                Player.STATE_IDLE -> PlaybackState.IDLE
                Player.STATE_BUFFERING -> PlaybackState.LOADING
                Player.STATE_READY -> if (player.playWhenReady) PlaybackState.PLAYING else PlaybackState.PAUSED
                Player.STATE_ENDED -> PlaybackState.IDLE
                else -> PlaybackState.IDLE
            }
            // ExoPlayer always transitions to STATE_IDLE after an error, and also
            // publishes a transient STATE_IDLE after player.stop() during play().
            // Don't let that IDLE overwrite ERROR or the fresh LOADING screen
            // (the IDLE→LOADING flicker was visible as a one-frame blank state).
            if (newState == PlaybackState.IDLE &&
                (_playbackState.value == PlaybackState.ERROR || _playbackState.value == PlaybackState.LOADING)) return
            if (newState != _playbackState.value) {
                DebugLogger.i(TAG, "state ${_playbackState.value} → $newState")
                _playbackState.value = newState
                if (newState == PlaybackState.PLAYING) {
                    // Success — clear error and reset retry counter.
                    _lastErrorMessage.value = null
                    retry.reset()
                }
            }
        }
        override fun onPlayerError(error: PlaybackException) {
            // Log full error details for diagnostics.
            val causeChain = buildString {
                var e: Throwable? = error
                while (e != null) {
                    append("← ${e.javaClass.simpleName}: ${e.message}")
                    e = e.cause
                }
            }
            DebugLogger.e(TAG, "error code=${error.errorCode} msg=${error.message} cause=$causeChain")
            _lastErrorMessage.value = error.message ?: "Playback error (code ${error.errorCode})"
            _playbackState.value = PlaybackState.ERROR
            if (error.errorCode == PlaybackException.ERROR_CODE_BEHIND_LIVE_WINDOW) {
                DebugLogger.w(TAG, "behind live window → seeking to live edge and re-preparing")
                _behindLiveWindow.value = true
                // After an error ExoPlayer is in STATE_IDLE; seekToDefaultPosition
                // alone does NOT resume — prepare() is required to leave IDLE.
                player.seekToDefaultPosition()
                player.prepare()
                player.play()
                _playbackState.value = PlaybackState.LOADING
                return
            }
            // Auto-retry ALL errors (not just network).  ERROR_CODE_UNSPECIFIED (1000)
            // and other internal ExoPlayer failures often resolve on restart.
            scheduleRetry()
        }
        override fun onIsPlayingChanged(isPlaying: Boolean) {
            if (isPlaying && _playbackState.value != PlaybackState.PLAYING) {
                _playbackState.value = PlaybackState.PLAYING
            }
        }
    }

    init {
        player.addListener(listener)

        scope.launch {
            while (isActive) { updateTimeline(); delay(100) }
        }
    }

    private fun scheduleRetry() {
        val url = currentHlsUrl ?: return
        if (!retry.canRetry()) {
            DebugLogger.w(TAG, "max retries ($MAX_RETRIES) reached — giving up")
            return
        }
        retryJob?.cancel()
        retryJob = scope.launch {
            // nextDelayMs() advances the counter; the auto-retry path MUST NOT
            // reset it (reconnect() does not call retry.reset()), so the backoff
            // actually escalates and MAX_RETRIES is eventually reached.
            val delayMs = retry.nextDelayMs() ?: return@launch
            DebugLogger.i(TAG, "auto-retry #${retry.retryCount} in ${delayMs}ms (url=$url)")
            delay(delayMs)
            DebugLogger.i(TAG, "auto-retry #${retry.retryCount} — attempting reconnect")
            reconnect(url)
        }
    }

    private fun updateTimeline() {
        if (player.playbackState != Player.STATE_READY) return
        val timeline = player.currentTimeline
        if (timeline.isEmpty) return
        val window = Timeline.Window()
        val idx = player.currentMediaItemIndex
        if (idx < 0 || idx >= timeline.windowCount) {
            // HLS playlist was replaced with a shorter one while we held an old index.
            // Reset to the default position so the next update picks up the live edge.
            DebugLogger.w(TAG, "updateTimeline: currentMediaItemIndex=$idx out of bounds (windowCount=${timeline.windowCount}) — playlist likely shrunk")
            player.seekToDefaultPosition()
            return
        }
        timeline.getWindow(idx, window)
        if (window.windowStartTimeMs != C.TIME_UNSET) {
            _currentTimelineMs.value = window.windowStartTimeMs + player.currentPosition
        }
    }

    /**
     * Manual (user-initiated) play: resets the retry counter so a fresh
     * reconnect attempt after an error starts the backoff from scratch.
     */
    override fun play(hlsUrl: String) {
        DebugLogger.i(TAG, "play url=$hlsUrl")
        retry.reset()
        prepareAndPlay(hlsUrl)
    }

    /**
     * Auto-retry reconnect: same media setup as [play] but does NOT reset the
     * retry counter (so backoff escalates and MAX_RETRIES is reachable).
     */
    private fun reconnect(hlsUrl: String) {
        DebugLogger.i(TAG, "reconnect url=$hlsUrl")
        prepareAndPlay(hlsUrl)
    }

    private fun prepareAndPlay(hlsUrl: String) {
        currentHlsUrl = hlsUrl
        retryJob?.cancel()
        retryJob = null
        _lastErrorMessage.value = null
        player.stop()  // force clean reset through IDLE → BUFFERING → READY
        _playbackState.value = PlaybackState.LOADING
        player.setMediaItem(MediaItem.Builder().setUri(hlsUrl)
            .setMediaMetadata(
                MediaMetadata.Builder()
                    .setTitle("CRI Radio")
                    .setArtist("Live Broadcast")
                    .build()
            )
            .setLiveConfiguration(
                // targetOffset makes ExoPlayer START at (live edge − offset) and hold
                // it there via the min/max speed band. Subtitles are in lockstep with
                // audio, so any position inside the window has matching metadata; a
                // ~20s offset leaves a comfortable band of upcoming subtitle text below
                // the active word for the karaoke scroll. The offset is relative to the
                // live edge, so it stays inside the seekable window on young servers too
                // (ExoPlayer clamps to the window start when the archive is shorter).
                MediaItem.LiveConfiguration.Builder()
                    .setTargetOffsetMs(LIVE_OFFSET_MS)
                    .setMaxPlaybackSpeed(1.02f)
                    .setMinPlaybackSpeed(0.98f)
                    .build()
            ).build())
        player.prepare()
        player.play()
    }

    override fun pause() {
        DebugLogger.i(TAG, "pause at=${_currentTimelineMs.value}ms")
        pausedAtTimelineMs = _currentTimelineMs.value
        _playbackState.value = PlaybackState.PAUSED
        player.pause()
    }

    override fun resume() {
        DebugLogger.i(TAG, "resume pausedAt=${pausedAtTimelineMs}ms")
        // After an error ExoPlayer sits in STATE_IDLE: player.play() only flips
        // playWhenReady and does not resume. prepare() is required to restart.
        if (player.playbackState == Player.STATE_IDLE) {
            val url = currentHlsUrl
            if (url != null) {
                DebugLogger.w(TAG, "resume: player IDLE — re-preparing $url")
                prepareAndPlay(url)
                return
            }
            player.prepare()
            player.play()
            return
        }
        val window = Timeline.Window()
        val timeline = player.currentTimeline
        if (timeline.isEmpty) { player.play(); return }
        val idx = player.currentMediaItemIndex
        if (idx < 0 || idx >= timeline.windowCount) {
            DebugLogger.w(TAG, "resume: currentMediaItemIndex=$idx out of bounds (windowCount=${timeline.windowCount}) — seeking to default")
            player.seekToDefaultPosition()
            player.play()
            return
        }
        timeline.getWindow(idx, window)
        if (pausedAtTimelineMs > 0 && window.windowStartTimeMs != C.TIME_UNSET) {
            if (pausedAtTimelineMs < window.windowStartTimeMs) {
                DebugLogger.w(TAG, "paused position fell behind DVR window")
                _behindLiveWindow.value = true
                seekToLiveEdge()
            } else {
                player.seekTo(pausedAtTimelineMs - window.windowStartTimeMs)
            }
        }
        player.play()
    }

    override fun seekTo(timelineMs: Long) {
        DebugLogger.d(TAG, "seekTo $timelineMs")
        pausedAtTimelineMs = timelineMs  // remember so resume() doesn't jump back
        val window = Timeline.Window()
        val timeline = player.currentTimeline
        if (timeline.isEmpty) return
        val idx = player.currentMediaItemIndex
        if (idx < 0 || idx >= timeline.windowCount) {
            DebugLogger.w(TAG, "seekTo: currentMediaItemIndex=$idx out of bounds (windowCount=${timeline.windowCount})")
            player.seekToDefaultPosition()
            return
        }
        timeline.getWindow(idx, window)
        if (window.windowStartTimeMs != C.TIME_UNSET) {
            player.seekTo((timelineMs - window.windowStartTimeMs).coerceAtLeast(0))
        }
    }

    override fun seekToLiveEdge() {
        DebugLogger.i(TAG, "seekToLiveEdge")
        player.seekToDefaultPosition()
        _behindLiveWindow.value = false
    }

    override fun release() {
        DebugLogger.i(TAG, "release")
        retryJob?.cancel()
        scope.cancel()
        player.removeListener(listener)
        player.release()
    }

    companion object {
        private const val MAX_RETRIES = 10
        private const val RETRY_BASE_DELAY_MS = 1000L  // 1s base
        private const val RETRY_MAX_DELAY_MS = 32_000L // cap at 32s
        /** Start playback this far behind the live edge (subtitles are in lockstep). */
        private const val LIVE_OFFSET_MS = 20_000L
    }
}

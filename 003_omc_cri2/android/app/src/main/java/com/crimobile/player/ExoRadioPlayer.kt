package com.crimobile.player

import android.content.Context
import android.util.Log
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.common.Timeline
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.DefaultMediaSourceFactory
import com.crimobile.model.PlaybackState
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

private const val TAG = "CRIRadio:player"

class ExoRadioPlayer(context: Context) : RadioPlayer {

    private val player: ExoPlayer = ExoPlayer.Builder(context)
        .setMediaSourceFactory(DefaultMediaSourceFactory(context).setLiveTargetOffsetMs(3000))
        .build()

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
    private var retryCount = 0
    private var retryJob: Job? = null

    init {
        player.addListener(object : Player.Listener {
            override fun onPlaybackStateChanged(state: Int) {
                val newState = when (state) {
                    Player.STATE_IDLE -> PlaybackState.IDLE
                    Player.STATE_BUFFERING -> PlaybackState.LOADING
                    Player.STATE_READY -> if (player.playWhenReady) PlaybackState.PLAYING else PlaybackState.PAUSED
                    Player.STATE_ENDED -> PlaybackState.IDLE
                    else -> PlaybackState.IDLE
                }
                // ExoPlayer always transitions to STATE_IDLE after an error.
                // Don't overwrite ERROR — the error screen must stay visible
                // until the user retries or auto-retry succeeds.
                if (newState == PlaybackState.IDLE && _playbackState.value == PlaybackState.ERROR) return
                if (newState != _playbackState.value) {
                    Log.i(TAG, "state ${_playbackState.value} → $newState")
                    _playbackState.value = newState
                    if (newState == PlaybackState.PLAYING) {
                        // Success — clear error and reset retry counter
                        _lastErrorMessage.value = null
                        retryCount = 0
                    }
                }
            }
            override fun onPlayerError(error: PlaybackException) {
                Log.e(TAG, "error code=${error.errorCode} msg=${error.message}")
                _lastErrorMessage.value = error.message ?: "Playback error (code ${error.errorCode})"
                _playbackState.value = PlaybackState.ERROR
                if (error.errorCode == PlaybackException.ERROR_CODE_BEHIND_LIVE_WINDOW) {
                    Log.w(TAG, "behind live window → seeking to live edge")
                    _behindLiveWindow.value = true
                    seekToLiveEdge()
                }
                // Auto-retry network errors with exponential backoff
                if (error.errorCode == PlaybackException.ERROR_CODE_IO_NETWORK_CONNECTION_FAILED
                    || error.errorCode == PlaybackException.ERROR_CODE_IO_NETWORK_CONNECTION_TIMEOUT) {
                    scheduleRetry()
                }
            }
            override fun onIsPlayingChanged(isPlaying: Boolean) {
                if (isPlaying && _playbackState.value != PlaybackState.PLAYING) {
                    _playbackState.value = PlaybackState.PLAYING
                }
            }
        })

        scope.launch {
            while (isActive) { updateTimeline(); delay(100) }
        }
    }

    private fun scheduleRetry() {
        val url = currentHlsUrl ?: return
        if (retryCount >= MAX_RETRIES) {
            Log.w(TAG, "max retries ($MAX_RETRIES) reached — giving up")
            return
        }
        retryJob?.cancel()
        retryJob = scope.launch {
            val delayMs = RETRY_BASE_DELAY_MS * (1L shl retryCount)
            retryCount++
            Log.i(TAG, "auto-retry #$retryCount in ${delayMs}ms (url=$url)")
            delay(delayMs)
            Log.i(TAG, "auto-retry #$retryCount — attempting reconnect")
            play(url)
        }
    }

    private fun updateTimeline() {
        if (player.playbackState != Player.STATE_READY) return
        val timeline = player.currentTimeline
        if (timeline.isEmpty) return
        val window = Timeline.Window()
        timeline.getWindow(player.currentMediaItemIndex, window)
        if (window.windowStartTimeMs != C.TIME_UNSET) {
            _currentTimelineMs.value = window.windowStartTimeMs + player.currentPosition
        }
    }

    override fun play(hlsUrl: String) {
        Log.i(TAG, "play url=$hlsUrl")
        currentHlsUrl = hlsUrl
        retryCount = 0  // reset on manual play
        retryJob?.cancel()
        retryJob = null
        _lastErrorMessage.value = null
        player.stop()  // force clean reset through IDLE → BUFFERING → READY
        _playbackState.value = PlaybackState.LOADING
        player.setMediaItem(MediaItem.Builder().setUri(hlsUrl).setLiveConfiguration(
            MediaItem.LiveConfiguration.Builder().setMaxPlaybackSpeed(1.02f).setMinPlaybackSpeed(0.98f).build()
        ).build())
        player.prepare()
        player.play()
    }

    override fun pause() {
        Log.i(TAG, "pause at=${_currentTimelineMs.value}ms")
        pausedAtTimelineMs = _currentTimelineMs.value
        _playbackState.value = PlaybackState.PAUSED
        player.pause()
    }

    override fun resume() {
        Log.i(TAG, "resume pausedAt=${pausedAtTimelineMs}ms")
        val window = Timeline.Window()
        val timeline = player.currentTimeline
        if (timeline.isEmpty) { player.play(); return }
        timeline.getWindow(player.currentMediaItemIndex, window)
        if (pausedAtTimelineMs > 0 && window.windowStartTimeMs != C.TIME_UNSET) {
            if (pausedAtTimelineMs < window.windowStartTimeMs) {
                Log.w(TAG, "paused position fell behind DVR window")
                _behindLiveWindow.value = true
                seekToLiveEdge()
            } else {
                player.seekTo(pausedAtTimelineMs - window.windowStartTimeMs)
            }
        }
        player.play()
    }

    override fun seekTo(timelineMs: Long) {
        Log.d(TAG, "seekTo $timelineMs")
        pausedAtTimelineMs = timelineMs  // remember so resume() doesn't jump back
        val window = Timeline.Window()
        val timeline = player.currentTimeline
        if (timeline.isEmpty) return
        timeline.getWindow(player.currentMediaItemIndex, window)
        if (window.windowStartTimeMs != C.TIME_UNSET) {
            player.seekTo((timelineMs - window.windowStartTimeMs).coerceAtLeast(0))
        }
    }

    override fun seekToLiveEdge() {
        Log.i(TAG, "seekToLiveEdge")
        player.seekToDefaultPosition()
        _behindLiveWindow.value = false
    }

    override fun release() {
        Log.i(TAG, "release")
        retryJob?.cancel()
        player.release()
    }

    companion object {
        private const val MAX_RETRIES = 3
        private const val RETRY_BASE_DELAY_MS = 1000L  // 1s, 2s, 4s
    }
}

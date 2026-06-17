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

    private var pausedAtTimelineMs: Long = 0L

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
                if (newState != _playbackState.value) {
                    Log.i(TAG, "state ${_playbackState.value} → $newState")
                    _playbackState.value = newState
                }
            }
            override fun onPlayerError(error: PlaybackException) {
                Log.e(TAG, "error code=${error.errorCode} msg=${error.message}")
                _playbackState.value = PlaybackState.ERROR
                if (error.errorCode == PlaybackException.ERROR_CODE_BEHIND_LIVE_WINDOW) {
                    Log.w(TAG, "behind live window → seeking to live edge")
                    _behindLiveWindow.value = true
                    seekToLiveEdge()
                }
            }
            override fun onIsPlayingChanged(isPlaying: Boolean) {
                if (_playbackState.value == PlaybackState.PAUSED && isPlaying) {
                    _playbackState.value = PlaybackState.PLAYING
                }
            }
        })

        scope.launch {
            while (isActive) { updateTimeline(); delay(100) }
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
        player.release()
    }
}

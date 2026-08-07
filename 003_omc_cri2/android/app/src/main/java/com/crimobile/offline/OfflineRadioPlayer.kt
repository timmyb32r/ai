package com.crimobile.offline

import android.content.Context
import android.net.Uri
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.ConcatenatingMediaSource
import androidx.media3.exoplayer.source.DefaultMediaSourceFactory
import com.crimobile.model.PlaybackState
import com.crimobile.model.SegmentMeta
import com.crimobile.player.RadioPlayer
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

/**
 * Implements [RadioPlayer] using local .ts audio files via
 * ExoPlayer's [ConcatenatingMediaSource].
 *
 * Timeline mapping between absolute epoch-milliseconds (used by
 * SubtitleSyncEngine) and local ExoPlayer positions is delegated to
 * [OfflineTimelineMapper] — a pure, tested component that keeps the
 * ordered-segment list and the prefix-sum offset table in sync and sorted
 * chronologically by `timeline_start_sec`.
 *
 * Non-contiguous segments (gaps in the archive) are handled
 * naturally — each segment is a separate MediaItem with its
 * own timeline.
 */
class OfflineRadioPlayer(
    segments: List<SegmentMeta>,
    private val storageManager: OfflineStorageManager,
    private val sessionId: String,
    context: Context
) : RadioPlayer {

    private val player: ExoPlayer = ExoPlayer.Builder(context).build().apply {
        // Offline playback reads local files — keep the CPU awake but do NOT hold
        // Wi-Fi awake (WAKE_MODE_NETWORK would waste power on a local-only stream).
        setWakeMode(C.WAKE_MODE_LOCAL)
    }
    private val scope = CoroutineScope(Dispatchers.Main)

    // ── Segment offset mapping (delegated to OfflineTimelineMapper) ──────
    private val mapper: OfflineTimelineMapper
    private val orderedSegments: List<SegmentMeta>
    private val segmentOffsetsMs: LongArray
    private var builtCount = 0
    private val isContinuous: Boolean         // true → single concatenated file, no multi-window API

    init {
        // Discover available audio in ONE directory listing (avoids N per-segment
        // File.exists()/length() calls on the main thread).
        val audioDir = storageManager.sessionAudioDir(sessionId)
        val audioFiles = (audioDir.listFiles { f ->
            f.name.endsWith(".ts") && f.name != "continuous.ts" && f.length() > 0
        } ?: emptyArray())
        val audioFileById = audioFiles.mapNotNull { f ->
            val id = f.name.substringBefore('.').toIntOrNull() ?: return@mapNotNull null
            id to f
        }.toMap()

        val available = segments.filter { it.segment_id in audioFileById }

        mapper = OfflineTimelineMapper(available)
        orderedSegments = mapper.orderedSegments
        segmentOffsetsMs = mapper.segmentOffsetsMs

        DebugLogger.i(TAG, "init ${orderedSegments.size} segments (${segments.size} total, " +
            "${segments.size - orderedSegments.size} missing audio)")

        // Play a single concatenated file when available (gapless by
        // construction — one decoder for the entire stream).  Fall back
        // to per-segment ConcatenatingMediaSource for old sessions.
        var useContinuous = false
        if (orderedSegments.isNotEmpty()) {
            val concatFile = storageManager.getConcatenatedAudioFile(sessionId)
            if (concatFile != null) {
                // Single continuous file → one decoder, zero gaps.
                val factory = DefaultMediaSourceFactory(context)
                player.setMediaSource(factory.createMediaSource(MediaItem.fromUri(Uri.fromFile(concatFile))))
                useContinuous = true
                DebugLogger.i(TAG, "using continuous.ts — single-source gapless playback")
            } else {
                // Legacy: per-segment files — still try gapless.
                val concat = ConcatenatingMediaSource(/* isGapless = */ true)
                val factory = DefaultMediaSourceFactory(context)
                for (seg in orderedSegments) {
                    val file = audioFileById[seg.segment_id] ?: continue
                    val mediaSource = factory.createMediaSource(MediaItem.fromUri(Uri.fromFile(file)))
                    concat.addMediaSource(mediaSource)
                    builtCount++
                }
                player.setMediaSource(concat)
                DebugLogger.i(TAG, "using ${orderedSegments.size} segments via ConcatenatingMediaSource (gapless)")
            }
            player.prepare()
        }
        isContinuous = useContinuous
    }

    // ── State flows ────────────────────────────────────────────────────

    private val _currentTimelineMs = MutableStateFlow(0L)
    override val currentTimelineMs: StateFlow<Long> = _currentTimelineMs.asStateFlow()

    private val _playbackState = MutableStateFlow(PlaybackState.IDLE)
    override val playbackState: StateFlow<PlaybackState> = _playbackState.asStateFlow()

    private val _behindLiveWindow = MutableStateFlow(false)
    override val behindLiveWindow: StateFlow<Boolean> = _behindLiveWindow.asStateFlow()

    private val _lastErrorMessage = MutableStateFlow<String?>(null)
    override val lastErrorMessage: StateFlow<String?> = _lastErrorMessage.asStateFlow()

    private var timelineJob: Job? = null

    private val listener = object : Player.Listener {
        override fun onPlaybackStateChanged(state: Int) {
            val newState = when (state) {
                Player.STATE_IDLE -> PlaybackState.IDLE
                Player.STATE_BUFFERING -> PlaybackState.LOADING
                Player.STATE_READY -> if (player.playWhenReady) PlaybackState.PLAYING else PlaybackState.PAUSED
                Player.STATE_ENDED -> PlaybackState.IDLE
                else -> PlaybackState.IDLE
            }
            if (newState != _playbackState.value) {
                DebugLogger.d(TAG, "state ${_playbackState.value} → $newState")
                _playbackState.value = newState
            }
        }

        override fun onPlayerError(error: PlaybackException) {
            DebugLogger.e(TAG, "error code=${error.errorCode} msg=${error.message}")
            _lastErrorMessage.value = error.message ?: "Offline playback error"
            _playbackState.value = PlaybackState.ERROR
        }

        override fun onIsPlayingChanged(isPlaying: Boolean) {
            if (_playbackState.value == PlaybackState.PAUSED && isPlaying) {
                _playbackState.value = PlaybackState.PLAYING
            }
        }
    }

    init {
        player.addListener(listener)

        // Poll timeline at ~10 Hz
        timelineJob = scope.launch {
            while (isActive) {
                updateTimeline()
                delay(100)
            }
        }
    }

    // ── RadioPlayer implementation ─────────────────────────────────────

    override fun play(hlsUrl: String) {
        if (orderedSegments.isEmpty()) {
            _lastErrorMessage.value = "No offline audio files found"
            _playbackState.value = PlaybackState.ERROR
            return
        }
        DebugLogger.i(TAG, "play (offline) — ${orderedSegments.size} segments${if (isContinuous) " (continuous)" else ""}")
        _lastErrorMessage.value = null
        _playbackState.value = PlaybackState.LOADING
        player.play()
    }

    override fun pause() {
        DebugLogger.i(TAG, "pause at=${_currentTimelineMs.value}ms")
        _playbackState.value = PlaybackState.PAUSED
        player.pause()
    }

    override fun resume() {
        DebugLogger.i(TAG, "resume")
        player.play()
    }

    override fun seekTo(timelineMs: Long) {
        DebugLogger.d(TAG, "seekTo $timelineMs")
        if (orderedSegments.isEmpty()) return
        val target = mapper.seekTarget(timelineMs)
        if (target.segmentIndex < 0) {
            player.seekTo(0L)
            return
        }
        if (isContinuous) {
            // Single window → absolute position = prefix sum + offset.
            player.seekTo(target.absolutePositionMs.coerceAtLeast(0))
        } else {
            // Multi-window → decompose into (windowIndex, positionInWindow).
            player.seekTo(target.segmentIndex, target.offsetInSegmentMs.coerceAtLeast(0))
        }
    }

    override fun seekToLiveEdge() {
        DebugLogger.i(TAG, "seekToLiveEdge → last segment")
        if (orderedSegments.isNotEmpty()) {
            if (isContinuous) {
                // Start of the LAST segment — NOT segmentOffsetsMs.last() (which is
                // the total duration = a position past the end → STATE_ENDED).
                player.seekTo(mapper.liveEdgePositionMs())
            } else {
                val lastIdx = orderedSegments.size - 1
                player.seekTo(lastIdx, 0L)
            }
        }
    }

    override fun release() {
        DebugLogger.i(TAG, "release")
        timelineJob?.cancel()
        scope.cancel()
        player.removeListener(listener)
        player.release()
    }

    // ── Internal ───────────────────────────────────────────────────────

    private fun updateTimeline() {
        if (orderedSegments.isEmpty()) return
        if (player.playbackState != Player.STATE_READY && player.playbackState != Player.STATE_BUFFERING) return

        val totalPos: Long = if (isContinuous) {
            // Single file → single window → currentPosition is absolute.
            player.currentPosition
        } else {
            // Multi-window ConcatenatingMediaSource: prefix sum + per-window position.
            val windowIdx = player.currentMediaItemIndex
            if (windowIdx in 0 until orderedSegments.size) {
                segmentOffsetsMs[windowIdx] + player.currentPosition
            } else {
                player.currentPosition
            }
        }
        _currentTimelineMs.value = mapper.timelineMsForPosition(totalPos).coerceAtLeast(0)
    }

    companion object {
        private const val TAG = "CRIRadio:offlinePlayer"
    }
}

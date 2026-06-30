package com.crimobile.offline

import android.content.Context
import android.net.Uri
import android.util.Log
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.ConcatenatingMediaSource
import androidx.media3.exoplayer.source.ProgressiveMediaSource
import androidx.media3.datasource.FileDataSource
import com.crimobile.model.PlaybackState
import com.crimobile.model.SubtitleSegment
import com.crimobile.player.RadioPlayer
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

/**
 * Implements [RadioPlayer] using local .ts audio files via
 * ExoPlayer's [ConcatenatingMediaSource].
 *
 * Timeline mapping between absolute epoch-milliseconds (used by
 * SubtitleSyncEngine) and local ExoPlayer positions is handled by
 * maintaining a segment→offset lookup table built during init.
 *
 * Non-contiguous segments (gaps in the archive) are handled
 * naturally — each segment is a separate MediaItem with its
 * own timeline.
 */
class OfflineRadioPlayer(
    segments: List<SubtitleSegment>,
    private val storageManager: OfflineStorageManager,
    private val sessionId: String,
    context: Context
) : RadioPlayer {

    private val player: ExoPlayer = ExoPlayer.Builder(context).build()
    private val scope = CoroutineScope(Dispatchers.Main)

    // ── Segment offset mapping ──────────────────────────────────────────
    // Maintain two parallel arrays indexed by the order segments are added
    // to the concatenated source.
    private val orderedSegments: List<SubtitleSegment>
    private val segmentOffsetsMs: LongArray   // prefix sum: offsetMs[i] = total duration before segment i
    private var builtCount = 0

    init {
        // Build ordered list: only segments whose audio file exists
        val available = mutableListOf<SubtitleSegment>()
        val offsets = mutableListOf(0L)

        for (seg in segments) {
            val audioFile = storageManager.getAudioFile(sessionId, seg.segment_id)
            if (audioFile != null) {
                available.add(seg)
                val durMs = ((seg.timeline_end_sec - seg.timeline_start_sec) * 1000).toLong().coerceAtLeast(1)
                offsets.add(offsets.last() + durMs)
            }
        }

        orderedSegments = available
        segmentOffsetsMs = offsets.toLongArray()

        Log.i(TAG, "init ${orderedSegments.size} segments (${segments.size} total, " +
            "${segments.size - orderedSegments.size} missing audio)")

        // Build ConcatenatingMediaSource
        if (orderedSegments.isNotEmpty()) {
            val concat = ConcatenatingMediaSource()
            for (seg in orderedSegments) {
                val file = storageManager.getAudioFile(sessionId, seg.segment_id)!!
                val uri = Uri.fromFile(file)
                val mediaSource = ProgressiveMediaSource.Factory(
                    FileDataSource.Factory()
                ).createMediaSource(MediaItem.fromUri(uri))
                concat.addMediaSource(mediaSource)
                builtCount++
            }
            player.setMediaSource(concat)
            player.prepare()
        }
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
                    Log.d(TAG, "state ${_playbackState.value} → $newState")
                    _playbackState.value = newState
                }
            }

            override fun onPlayerError(error: PlaybackException) {
                Log.e(TAG, "error code=${error.errorCode} msg=${error.message}")
                _lastErrorMessage.value = error.message ?: "Offline playback error"
                _playbackState.value = PlaybackState.ERROR
            }

            override fun onIsPlayingChanged(isPlaying: Boolean) {
                if (_playbackState.value == PlaybackState.PAUSED && isPlaying) {
                    _playbackState.value = PlaybackState.PLAYING
                }
            }
        })

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
        if (builtCount == 0) {
            _lastErrorMessage.value = "No offline audio files found"
            _playbackState.value = PlaybackState.ERROR
            return
        }
        Log.i(TAG, "play (offline) — ${builtCount} segments")
        _lastErrorMessage.value = null
        _playbackState.value = PlaybackState.LOADING
        player.play()
    }

    override fun pause() {
        Log.i(TAG, "pause at=${_currentTimelineMs.value}ms")
        _playbackState.value = PlaybackState.PAUSED
        player.pause()
    }

    override fun resume() {
        Log.i(TAG, "resume")
        player.play()
    }

    override fun seekTo(timelineMs: Long) {
        Log.d(TAG, "seekTo $timelineMs")
        val idx = findSegmentForTimelineMs(timelineMs)
        if (idx < 0) {
            // Before all segments → seek to start of first window
            player.seekTo(0, 0L)
            return
        }
        val seg = orderedSegments[idx]
        val offsetInSeg = (timelineMs - (seg.timeline_start_sec * 1000).toLong())
            .coerceIn(0, ((seg.timeline_end_sec - seg.timeline_start_sec) * 1000).toLong())
        // Decompose absolute position into (windowIndex, positionInWindow)
        val posInWindow = offsetInSeg.coerceAtLeast(0)
        player.seekTo(idx, posInWindow)
    }

    override fun seekToLiveEdge() {
        Log.i(TAG, "seekToLiveEdge → last segment")
        if (orderedSegments.isNotEmpty()) {
            val lastIdx = orderedSegments.size - 1
            player.seekTo(lastIdx, 0L)
        }
    }

    override fun release() {
        Log.i(TAG, "release")
        timelineJob?.cancel()
        player.release()
    }

    // ── Internal ───────────────────────────────────────────────────────

    private fun updateTimeline() {
        if (builtCount == 0) return
        if (player.playbackState != Player.STATE_READY && player.playbackState != Player.STATE_BUFFERING) return

        // ExoPlayer.currentPosition is per-window in a ConcatenatingMediaSource.
        // Convert to absolute position: prefix sum for windows before current + position in current.
        val windowIdx = player.currentMediaItemIndex
        val totalPos = if (windowIdx in 0 until orderedSegments.size) {
            segmentOffsetsMs[windowIdx] + player.currentPosition
        } else {
            player.currentPosition
        }
        val idx = findSegmentForPosition(totalPos)
        if (idx < 0) {
            _currentTimelineMs.value = 0L
            return
        }
        val seg = orderedSegments[idx]
        val offsetInSeg = totalPos - segmentOffsetsMs[idx]
        _currentTimelineMs.value = (seg.timeline_start_sec * 1000 + offsetInSeg).toLong().coerceAtLeast(0)
    }

    /** Binary search: which segment contains [timelineMs] (absolute epoch ms). */
    private fun findSegmentForTimelineMs(timelineMs: Long): Int {
        var lo = 0
        var hi = orderedSegments.size - 1
        while (lo <= hi) {
            val mid = (lo + hi) / 2
            val seg = orderedSegments[mid]
            val segStart = (seg.timeline_start_sec * 1000).toLong()
            val segEnd = (seg.timeline_end_sec * 1000).toLong()
            when {
                timelineMs < segStart -> hi = mid - 1
                timelineMs >= segEnd -> lo = mid + 1
                else -> return mid
            }
        }
        // timelineMs is after all segments
        if (lo >= orderedSegments.size) return orderedSegments.size - 1
        // timelineMs is before all segments
        if (hi < 0) return -1
        return hi
    }

    /** Binary search: which segment contains [positionMs] (local concat position). */
    private fun findSegmentForPosition(positionMs: Long): Int {
        // segmentOffsetsMs has size = orderedSegments.size + 1
        // segmentOffsetsMs[i] = start offset of segment i
        // segmentOffsetsMs[last] = total duration
        var lo = 0
        var hi = orderedSegments.size - 1
        while (lo <= hi) {
            val mid = (lo + hi) / 2
            val segStart = segmentOffsetsMs[mid]
            val segEnd = segmentOffsetsMs[mid + 1]
            when {
                positionMs < segStart -> hi = mid - 1
                positionMs >= segEnd -> lo = mid + 1
                else -> return mid
            }
        }
        return -1
    }

    companion object {
        private const val TAG = "CRIRadio:offlinePlayer"
    }
}

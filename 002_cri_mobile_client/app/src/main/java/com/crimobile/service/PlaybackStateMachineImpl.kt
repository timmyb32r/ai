package com.crimobile.service

import android.app.Application
import android.content.Intent
import android.media.AudioTrack
import android.os.Build
import android.util.Log
import androidx.media3.common.MediaItem
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.exoplayer.ExoPlayer
import com.crimobile.PlaybackService
import com.crimobile.ServerConfig
import com.crimobile.api.HighlightTracker
import com.crimobile.api.PlaybackStateMachine
import com.crimobile.api.SubtitleStreamSource
import com.crimobile.infrastructure.PCMRingBuffer
import com.crimobile.model.PlaybackState
import com.crimobile.model.SubtitleEvent
import com.crimobile.model.SyncedSegment
import com.crimobile.model.WordBoundary
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeout
import java.io.IOException
import java.io.InputStream
import java.net.HttpURLConnection
import java.net.URL
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Media3 ExoPlayer playback engine — replaces the old daemon-thread PCM
 * loop with a standard Android media player for Opus/OGG audio streaming.
 *
 * ## Architecture
 * - **ExoPlayer** for main audio (Opus/OGG from `/v1/stream/audio.opus`)
 * - **SSE** for subtitle sync (timeline anchor via `sync` event)
 * - **PCMRingBuffer** preserved for word pronunciation (background PCM fill
 *   from the old `/v1/stream/audio` endpoint)
 * - **Sync loop** at 10 Hz via coroutine — `player.currentPosition` +
 *   `audioTimelineStartSec` for drift-free subtitle alignment
 *
 * ## Ordering
 * SSE is connected FIRST, then we wait for the sync event, then ExoPlayer
 * starts.  This guarantees the timeline anchor is available before any audio
 * is played.
 */
class PlaybackStateMachineImpl(
    private val app: Application,
    private val serverConfig: ServerConfig,
    private val subtitleSource: SubtitleStreamSource,
    private val highlightTracker: HighlightTracker,
    private val pcmBuffer: PCMRingBuffer,
    private val exoPlayer: ExoPlayer,
) : PlaybackStateMachine {

    private val _state = MutableStateFlow(PlaybackState.IDLE)
    override val state: StateFlow<PlaybackState> = _state

    private val _segmentsFlow = MutableStateFlow<List<SyncedSegment>>(emptyList())
    override val segments: StateFlow<List<SyncedSegment>> = _segmentsFlow

    private val _highlightRange = MutableStateFlow<IntRange?>(null)
    override val highlightRange: StateFlow<IntRange?> = _highlightRange

    private val _debugLog = MutableStateFlow<List<String>>(emptyList())
    override val debugLog: StateFlow<List<String>> = _debugLog

    private val _audioFlowing = MutableStateFlow(false)
    override val audioFlowing: StateFlow<Boolean> = _audioFlowing

    private val mainScope = CoroutineScope(SupervisorJob() + Dispatchers.Main)
    private val ioScope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    private var syncReady = false
    private var audioTimelineStartSec: Double? = null

    private val subtitles = mutableListOf<SubtitleEvent>()
    private var lastSegmentsHash = 0

    // ── Background PCM fill for pronounce ───────────────────────────────
    @Volatile private var pcmFillRunning = false
    @Volatile private var pcmConnection: HttpURLConnection? = null

    // ── ExoPlayer listener ──────────────────────────────────────────────

    private val playerListener = object : Player.Listener {
        override fun onPlaybackStateChanged(playbackState: Int) {
            val name = when (playbackState) {
                Player.STATE_IDLE -> "IDLE"
                Player.STATE_BUFFERING -> "BUFFERING"
                Player.STATE_READY -> "READY"
                Player.STATE_ENDED -> "ENDED"
                else -> "UNKNOWN($playbackState)"
            }
            logd("player → $name  (appState=${_state.value})")
            when (playbackState) {
                Player.STATE_READY -> {
                    if (_state.value == PlaybackState.LOADING && syncReady) {
                        _state.value = PlaybackState.PLAYING
                        _audioFlowing.value = true
                        val pos = exoPlayer.currentPosition
                        val anchor = audioTimelineStartSec ?: 0.0
                        logd("▶ PLAYING  position=${pos}ms  timeline=%.1f  buffered=${exoPlayer.bufferedPosition}ms".format(anchor + pos/1000.0))
                    }
                }
                Player.STATE_BUFFERING -> {
                    // Log when we transition TO buffering from a non-idle state (potential underrun)
                    if (_state.value == PlaybackState.PLAYING) {
                        val pos = exoPlayer.currentPosition
                        val buffered = exoPlayer.bufferedPosition
                        val gap = buffered - pos
                        Log.w(TAG, "⚠ BUFFERING  position=${pos}ms  buffered=${buffered}ms  gap=${gap}ms  muted=${!exoPlayer.playWhenReady}")
                        _audioFlowing.value = true
                    }
                }
                Player.STATE_ENDED -> {
                    Log.w(TAG, "✕ ENDED — stream terminated, reconnecting")
                    reconnect()
                }
            }
        }

        override fun onPlayerError(error: PlaybackException) {
            Log.e(TAG, "✕ player error  code=${error.errorCode}  " +
                "type=${error::class.simpleName}  msg=${error.message}  " +
                "cause=${error.cause}", error)
            // Log detailed error info
            when (error.errorCode) {
                PlaybackException.ERROR_CODE_IO_NETWORK_CONNECTION_FAILED ->
                    Log.e(TAG, "  → NETWORK_CONNECTION_FAILED — check server reachability")
                PlaybackException.ERROR_CODE_IO_NETWORK_CONNECTION_TIMEOUT ->
                    Log.e(TAG, "  → NETWORK_CONNECTION_TIMEOUT — server not responding")
                PlaybackException.ERROR_CODE_IO_BAD_HTTP_STATUS ->
                    Log.e(TAG, "  → BAD_HTTP_STATUS — check endpoint URL")
                PlaybackException.ERROR_CODE_DECODER_INIT_FAILED ->
                    Log.e(TAG, "  → DECODER_INIT_FAILED — Opus codec unavailable?")
                PlaybackException.ERROR_CODE_DECODER_QUERY_FAILED ->
                    Log.e(TAG, "  → DECODER_QUERY_FAILED — stream format mismatch")
                PlaybackException.ERROR_CODE_DECODING_FAILED ->
                    Log.e(TAG, "  → DECODING_FAILED — corrupted OGG/Opus data?")
                PlaybackException.ERROR_CODE_AUDIO_TRACK_INIT_FAILED ->
                    Log.e(TAG, "  → AUDIO_TRACK_INIT_FAILED — AudioTrack unavailable")
                PlaybackException.ERROR_CODE_AUDIO_TRACK_WRITE_FAILED ->
                    Log.e(TAG, "  → AUDIO_TRACK_WRITE_FAILED — AudioTrack buffer issue")
                else ->
                    Log.e(TAG, "  → errorCode=${error.errorCode}")
            }
            reconnect()
        }

        override fun onAudioSessionIdChanged(audioSessionId: Int) {
            logd("audio session: $audioSessionId")
        }

        override fun onIsPlayingChanged(isPlaying: Boolean) {
            logd("isPlaying=$isPlaying  muted=${!exoPlayer.playWhenReady}")
        }
    }

    // ── State transitions ──────────────────────────────────────────────

    private var playStartWallMs = 0L

    override fun play() {
        if (_state.value == PlaybackState.PLAYING || _state.value == PlaybackState.LOADING) return
        val wasPaused = _state.value == PlaybackState.PAUSED

        if (!wasPaused) {
            subtitles.clear()
            _segmentsFlow.value = emptyList()
            _highlightRange.value = null
        }

        playStartWallMs = System.currentTimeMillis()
        Log.d(TAG, "▶ play()  wasPaused=$wasPaused  opusUrl=${serverConfig.baseUrl}/v1/stream/audio.opus")
        _state.value = PlaybackState.LOADING
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            app.startForegroundService(Intent(app, PlaybackService::class.java))
        } else {
            app.startService(Intent(app, PlaybackService::class.java))
        }

        // Step 1: Connect SSE first to get the timeline anchor.
        subtitleSource.connect(serverConfig.baseUrl)

        // Step 2: Wait for the SSE sync event (with 5s timeout, matches server).
        mainScope.launch {
            try {
                withTimeout(5_000L) {
                    subtitleSource.audioTimelineStartSec
                        .filterNotNull()
                        .first()
                }
                val syncWaitMs = System.currentTimeMillis() - playStartWallMs
                audioTimelineStartSec = subtitleSource.audioTimelineStartSec.value
                syncReady = true
                Log.d(TAG, "⚓ sync: anchor=$audioTimelineStartSec  wait=${syncWaitMs}ms  " +
                    "playerPos=${exoPlayer.currentPosition}ms  playerState=${exoPlayer.playbackState}")

                // Step 3: Now start ExoPlayer.
                val opusUrl = "${serverConfig.baseUrl}/v1/stream/audio.opus"
                Log.d(TAG, "→ prepare: $opusUrl")
                val mediaItem = MediaItem.fromUri(opusUrl)
                exoPlayer.setMediaItem(mediaItem)
                exoPlayer.prepare()
                exoPlayer.playWhenReady = if (wasPaused) false else true

                // Step 4: Start the sync loop.
                startSyncLoop()

                // Start background PCM fill for pronounce.
                Log.d(TAG, "→ startPcmFill")
                startPcmFill()
            } catch (e: Exception) {
                val elapsed = System.currentTimeMillis() - playStartWallMs
                Log.e(TAG, "✕ sync failed after ${elapsed}ms: ${e.message}", e)
                _state.value = PlaybackState.IDLE
            }
        }
    }

    override fun pause() {
        if (_state.value != PlaybackState.PLAYING) return
        Log.d(TAG, "⏸ pause  pos=${exoPlayer.currentPosition}ms")
        _state.value = PlaybackState.PAUSED
        exoPlayer.playWhenReady = false
        _audioFlowing.value = false
    }

    override fun resume() {
        if (_state.value != PlaybackState.PAUSED) return
        Log.d(TAG, "▶ resume  pos=${exoPlayer.currentPosition}ms")
        _state.value = PlaybackState.PLAYING
        exoPlayer.playWhenReady = true
        _audioFlowing.value = true
    }

    override fun stop() {
        val elapsed = System.currentTimeMillis() - playStartWallMs
        Log.d(TAG, "⏹ stop  elapsed=${elapsed}ms  pos=${exoPlayer.currentPosition}ms  " +
            "playerState=${exoPlayer.playbackState}")
        _state.value = PlaybackState.IDLE
        syncReady = false
        audioTimelineStartSec = null
        exoPlayer.stop()
        subtitleSource.disconnect()
        pcmFillRunning = false
        pcmConnection?.disconnect()
        pcmConnection = null
        pcmBuffer.clear()
        app.stopService(Intent(app, PlaybackService::class.java))
        _segmentsFlow.value = emptyList()
        _audioFlowing.value = false
        _highlightRange.value = null
        subtitles.clear()
    }

    override fun togglePlayPause() {
        when (_state.value) {
            PlaybackState.IDLE, PlaybackState.PAUSED -> play()
            PlaybackState.PLAYING -> pause()
            PlaybackState.LOADING -> stop()
        }
    }

    // ── Reconnect on JUMP or ExoPlayer error ───────────────────────────

    private fun reconnect() {
        Log.d(TAG, "Reconnecting...")
        exoPlayer.stop()
        subtitleSource.disconnect()
        syncReady = false
        audioTimelineStartSec = null
        _audioFlowing.value = false
        if (_state.value != PlaybackState.IDLE) {
            play()
        }
    }

    // ── Sync loop (replaces daemon playback thread) ────────────────────

    private fun startSyncLoop() {
        mainScope.launch {
            var lastStatsLog = 0L
            var lastUiSync = 0L

            while (_state.value != PlaybackState.IDLE) {
                val now = System.nanoTime()

                // Drain subtitles.
                val newEvents = subtitleSource.drainPending()
                subtitles.addAll(newEvents)

                // Handle JUMP event.
                if (subtitleSource.hasJumpEvent) {
                    Log.d(TAG, "Jump event — reconnecting")
                    reconnect()
                    return@launch
                }

                // Garbage-collect old subtitles.
                val anchor = audioTimelineStartSec ?: continue
                val currentPos = anchor + exoPlayer.currentPosition / 1000.0
                subtitles.removeAll { it.end < currentPos - 30.0 }

                // Update UI segments (every 500ms).
                if ((now - lastUiSync) > 500_000_000L) {
                    val newSegments = subtitles.takeLast(10)
                    val hash = newSegments.hashCode()
                    if (hash != lastSegmentsHash) {
                        _segmentsFlow.value = newSegments.map { sub ->
                            SyncedSegment(sub.start, sub.end, ByteArray(0), sub)
                        }
                        lastSegmentsHash = hash
                    }
                    lastUiSync = now
                }

                // Highlight (every iteration).
                if (_state.value == PlaybackState.PLAYING && syncReady) {
                    val sub = subtitles.firstOrNull { currentPos in it.start..it.end }
                    _highlightRange.value = if (sub != null) {
                        highlightTracker.update(sub, currentPos)
                    } else {
                        null
                    }
                }

                // Stats every 10s.
                if ((now - lastStatsLog) > 10_000_000_000L) {
                    val memMb = (Runtime.getRuntime().totalMemory() -
                        Runtime.getRuntime().freeMemory()) / 1048576
                    // Audio health diagnostics
                    val playerPos = exoPlayer.currentPosition
                    val bufferedPos = exoPlayer.bufferedPosition
                    val bufferGapMs = bufferedPos - playerPos
                    val suppressed = !exoPlayer.playWhenReady
                    val anchor = audioTimelineStartSec ?: 0.0
                    val timelinePos = anchor + playerPos / 1000.0
                    val stateName = when (exoPlayer.playbackState) {
                        Player.STATE_IDLE -> "IDLE"
                        Player.STATE_BUFFERING -> "BUF"
                        Player.STATE_READY -> "RDY"
                        Player.STATE_ENDED -> "END"
                        else -> "?"
                    }
                    val pcmTotalSec = pcmBuffer.getTotalWritten() /
                        PCMRingBuffer.BYTES_PER_SEC
                    Log.d(TAG, "stats: tl=%.1f  pos=${playerPos}ms  buf=${bufferedPos}ms  " +
                        "gap=${bufferGapMs}ms  muted=${suppressed}  pl=$stateName  " +
                        "subs=${subtitles.size}  pcm=${pcmTotalSec}s  mem=${memMb}MB".format(timelinePos))
                    // Warn on dangerous conditions
                    if (bufferGapMs < 500) {
                        Log.w(TAG, "⚠ LOW BUFFER: gap=${bufferGapMs}ms — risk of underrun")
                    }
                    if (suppressed) {
                        Log.w(TAG, "⚠ PLAYBACK MUTED — playWhenReady=false while state=$stateName")
                    }
                    lastStatsLog = now
                }

                delay(100) // 10 Hz
            }
        }.invokeOnCompletion {
            _audioFlowing.value = false
        }
    }

    // ── Word interactions ──────────────────────────────────────────────

    override fun onWordClick(wb: WordBoundary, subtitle: SubtitleEvent) {
        if (_state.value == PlaybackState.PLAYING) {
            _state.value = PlaybackState.PAUSED
            exoPlayer.playWhenReady = false
        }
    }

    override fun pronounceWord(wb: WordBoundary, subtitle: SubtitleEvent) {
        val d = subtitle.end - subtitle.start
        val (fs, ts) = if (wb.endSec > wb.startSec && wb.startSec > 0) {
            subtitle.start + wb.startSec to subtitle.start + wb.endSec
        } else {
            val tl = subtitle.textZh.length.coerceAtLeast(1)
            val ws = subtitle.start + (wb.charStart.toDouble() / tl) * d
            ws to (ws + maxOf((wb.charEnd - wb.charStart).toDouble() / tl * d, 0.25))
        }
        val dur = ts - fs
        if (dur <= 0.0) return
        val bytes = (dur * PCMRingBuffer.BYTES_PER_SEC).toInt().coerceAtLeast(320)
        val pcm = pcmBuffer.readRange(fs, bytes) ?: return
        Thread {
            try {
                val minBuf = AudioTrack.getMinBufferSize(
                    PCMRingBuffer.SAMPLE_RATE,
                    android.media.AudioFormat.CHANNEL_OUT_MONO,
                    android.media.AudioFormat.ENCODING_PCM_16BIT,
                )
                val bufSize = pcm.size.coerceIn(minBuf, minBuf * 10)
                val at = AudioTrack(
                    android.media.AudioAttributes.Builder()
                        .setContentType(android.media.AudioAttributes.CONTENT_TYPE_MUSIC)
                        .setUsage(android.media.AudioAttributes.USAGE_MEDIA).build(),
                    android.media.AudioFormat.Builder()
                        .setSampleRate(PCMRingBuffer.SAMPLE_RATE)
                        .setChannelMask(android.media.AudioFormat.CHANNEL_OUT_MONO)
                        .setEncoding(android.media.AudioFormat.ENCODING_PCM_16BIT).build(),
                    bufSize, AudioTrack.MODE_STATIC,
                    android.media.AudioManager.AUDIO_SESSION_ID_GENERATE,
                )
                at.write(pcm, 0, pcm.size.coerceAtMost(bufSize))
                at.play()
                Thread.sleep((dur * 1000).toLong() + 200)
                at.stop()
                at.release()
            } catch (e: Exception) {
                Log.e(TAG, "pronounce", e)
            }
        }.also { it.isDaemon = true; it.start() }
    }

    // ── Background PCM fill for pronounce ──────────────────────────────

    private fun startPcmFill() {
        if (pcmFillRunning) return
        pcmFillRunning = true
        ioScope.launch {
            try {
                val url = URL("${serverConfig.baseUrl}/v1/stream/audio")
                val conn = url.openConnection() as HttpURLConnection
                conn.setRequestProperty("Accept", "audio/L16")
                conn.readTimeout = 0
                conn.connect()
                pcmConnection = conn

                val inputStream: InputStream = conn.inputStream
                val buf = ByteArray(16384)
                var total = 0L
                var lastPcmLog = System.currentTimeMillis()
                var bytesSinceLog = 0L
                while (pcmFillRunning && _state.value != PlaybackState.IDLE) {
                    val n = inputStream.read(buf)
                    if (n < 0) {
                        Log.d(TAG, "PCM fill EOS at $total bytes (${total / PCMRingBuffer.BYTES_PER_SEC}s) — reconnecting")
                        conn.disconnect()
                        if (pcmFillRunning) {
                            startPcmFill()
                        }
                        return@launch
                    }
                    pcmBuffer.write(buf, 0, n)
                    total += n
                    bytesSinceLog += n
                    // Log PCM fill rate every 30s
                    val nowMs = System.currentTimeMillis()
                    val dt = nowMs - lastPcmLog
                    if (dt > 30_000) {
                        val rateBps = bytesSinceLog * 1000 / dt
                        val bufSec = pcmBuffer.getTotalWritten() / PCMRingBuffer.BYTES_PER_SEC
                        Log.d(TAG, "PCM fill: ${total/PCMRingBuffer.BYTES_PER_SEC}s total  " +
                            "rate=${rateBps}B/s (target=32000)  bufTail=${bufSec}s")
                        bytesSinceLog = 0
                        lastPcmLog = nowMs
                    }
                }
                inputStream.close()
                conn.disconnect()
            } catch (e: Exception) {
                if (pcmFillRunning) {
                    Log.e(TAG, "PCM fill error: ${e.message}")
                }
            }
        }
    }

    override fun seekRelative(deltaSec: Double) {} // stub

    private fun logd(msg: String) {
        val ts = SimpleDateFormat("HH:mm:ss.SSS", Locale.US).format(Date())
        _debugLog.value = listOf("$ts  $msg") + _debugLog.value.take(199)
        Log.d(TAG, msg)
    }

    override fun clearDebugLog() {
        _debugLog.value = emptyList()
    }

    companion object {
        private const val TAG = "PlaybackStateMachine"
    }
}

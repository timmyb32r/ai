package com.crimobile.viewmodel

import android.app.Application
import android.content.Context
import com.crimobile.debug.DebugLogger
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.crimobile.ServerConfig
import com.crimobile.model.*
import com.crimobile.offline.DownloadEngine
import com.crimobile.offline.DownloadProgress
import com.crimobile.offline.OfflineRadioPlayer
import com.crimobile.offline.OfflineStorageManager
import com.crimobile.offline.OfflineSubtitleSource
import com.crimobile.offline.SegmentCache
import com.crimobile.offline.SyncConfig
import com.crimobile.offline.SyncScheduler
import com.crimobile.player.RadioPlayer
import com.crimobile.player.RadioPlayerHolder
import com.crimobile.pronounce.PronunciationPlayer
import com.crimobile.subtitles.HttpSubtitleSource
import com.crimobile.subtitles.SseSubtitleSource
import com.crimobile.subtitles.SubtitleSource
import com.crimobile.sync.SubtitleSyncEngine
import com.crimobile.vocabulary.VocabularyStore
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.flatMapLatest
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

data class CriViewState(
    val playbackState: PlaybackState = PlaybackState.IDLE,
    val segments: List<SubtitleSegment> = emptyList(),
    val segmentsMeta: List<SegmentMeta> = emptyList(),
    val activeSegmentId: Int? = null,
    val activeWord: WordEntry? = null,
    val activeSegment: SubtitleSegment? = null,
    val showPinyin: Boolean = false,
    val fontSizeSp: Int = 22,  // subtitle font size in sp
    val showWordBoundaries: Boolean = false,  // subtle underline under words
    val showAudioBoundaries: Boolean = false,  // debug: show .ts file boundaries
    val pinyinFontSizeSp: Int = 9,  // pinyin font size in sp
    val dictFontSizeSp: Int = 14,  // dictionary bottom sheet font size in sp
    val debugEnabled: Boolean = false,  // true when .cri_debug file exists
    val logToFileEnabled: Boolean = false,  // redirect logs to file
    val metadataProtocol: String = "HTTP",  // "HTTP" or "SSE"
    val wordPopup: WordPopupState? = null,
    val isPronouncing: Boolean = false,  // true while PronounceWord audio plays
    val connectionStatus: ConnectionStatus = ConnectionStatus.DISCONNECTED,
    val error: String? = null,
    val subtitleDelaySec: Double = 0.0,  // how far behind live are subtitles
    val lastActiveWord: WordEntry? = null,  // remembered for recenter during silence gaps
    val playbackMode: PlaybackMode = PlaybackMode.LIVE_STREAMING,
    val syncConfig: SyncConfig = SyncConfig(),
    val downloadProgress: DownloadProgress? = null,  // non-null when download is active
    val archiveInfo: com.crimobile.offline.ArchiveInfo? = null,  // server archive bounds
    val offlinePositionMs: Long = 0L,  // current position in offline playback (epoch ms)
    val offlineDurationMs: Long = 0L,  // total duration of offline content (ms)
    val offlineLocalRangeSec: Pair<Double, Double>? = null,  // (oldest, newest) of downloaded segments in epoch seconds
    val showOfflineNavDialog: Boolean = false,
    val offlineSessions: List<OfflineSessionInfo> = emptyList(),
    val offlineSessionSegments: List<SegmentMeta> = emptyList(),
    val selectedOfflineSessionId: String? = null
)

data class OfflineSessionInfo(
    val sessionId: String,
    val startSec: Long,
    val durationSec: Int,
    val segmentCount: Int,
    val createdAt: Long
)

sealed class CriAction {
    data class Play(val serverUrl: String) : CriAction()
    object Pause : CriAction()
    object Resume : CriAction()
    data class WordTapped(val word: WordEntry, val segmentId: Int) : CriAction()
    object DismissPopup : CriAction()
    object PronounceWord : CriAction()
    object SaveWord : CriAction()
    object TogglePinyin : CriAction()
    data class SetFontSize(val sp: Int) : CriAction()
    object ToggleWordBoundaries : CriAction()
    object ToggleAudioBoundaries : CriAction()
    data class SetPinyinFontSize(val sp: Int) : CriAction()
    data class SetDictFontSize(val sp: Int) : CriAction()
    object EnableDebug : CriAction()
    data class SetPlaybackMode(val mode: PlaybackMode) : CriAction()
    data class UpdateSyncConfig(val config: SyncConfig) : CriAction()
    object LoadArchiveInfo : CriAction()
    object StartInitialSync : CriAction()
    object CancelDownload : CriAction()
    object OpenOfflineNavDialog : CriAction()
    object DismissOfflineNavDialog : CriAction()
    data class SelectOfflineSession(val sessionId: String) : CriAction()
    data class SelectOfflineSegment(val segmentId: Int) : CriAction()
    data class SetMetadataProtocol(val protocol: String) : CriAction() // "HTTP" or "SSE"
    object ToggleLogToFile : CriAction()  // debug: redirect logs to file
}

class CriViewModel(application: Application) : AndroidViewModel(application) {

    // Player is now owned by PlayerService (foreground service).
    // We obtain it via the singleton holder — same StateFlow, zero IPC latency.
    private lateinit var player: RadioPlayer

    private val prefs = application.getSharedPreferences("cri_prefs", Context.MODE_PRIVATE)

    // Swappable subtitle source — see createSubtitleSource() for factory logic.
    private val _subtitleSource = MutableStateFlow<SubtitleSource>(createSubtitleSource())
    private val subtitleSource: SubtitleSource get() = _subtitleSource.value
    private val vocabularyStore = VocabularyStore(application)
    private val pronunciationPlayer by lazy { PronunciationPlayer({ activePlayerOrNull() }, viewModelScope) }

    /** Build a [SubtitleSource] based on the stored metadata_protocol preference. */
    private fun createSubtitleSource(): SubtitleSource {
        val protocol = prefs.getString("metadata_protocol", "HTTP") ?: "HTTP"
        return if (protocol == "SSE") SseSubtitleSource() else HttpSubtitleSource()
    }

    private val _state = MutableStateFlow(
        CriViewState(
            showPinyin = prefs.getBoolean("show_pinyin", false),
            fontSizeSp = prefs.getInt("font_size_sp", 22),
            showWordBoundaries = prefs.getBoolean("show_word_boundaries", false),
            showAudioBoundaries = prefs.getBoolean("show_audio_boundaries", false),
            pinyinFontSizeSp = prefs.getInt("pinyin_font_size_sp", 9),
            dictFontSizeSp = prefs.getInt("dict_font_size_sp", 14),
            debugEnabled = prefs.getBoolean("debug_enabled", false),
            logToFileEnabled = prefs.getBoolean("log_to_file_enabled", false),
            metadataProtocol = prefs.getString("metadata_protocol", "HTTP") ?: "HTTP",
        )
    )

    // Apply persisted debug-log setting on startup.
    init { DebugLogger.enabled = prefs.getBoolean("log_to_file_enabled", false) }

    val state: StateFlow<CriViewState> = _state.asStateFlow()

    private val savedWord = MutableStateFlow<WordEntry?>(null)
    private var currentServerUrl: String = ""
    private var lastSyncLog = 0L
    private var lastActiveSegId = -1
    private var lastActiveWord: WordEntry? = null
    private var pendingPlayUrl: String? = null  // deferred Play until player is ready
    private var coldStartT0: Long = 0  // timing: System.nanoTime() when Play was tapped
    private val pendingDictFetches = mutableSetOf<Int>()  // segment IDs being lazy-fetched

    // ── Offline mode ───────────────────────────────────────────────────
    private val offlineStorageManager by lazy { OfflineStorageManager(getApplication()) }
    private val offlineSubtitleSource by lazy { OfflineSubtitleSource(offlineStorageManager) }
    private var offlinePlayer: OfflineRadioPlayer? = null
    var segmentCache: SegmentCache? = null
        private set
    private var downloadJob: kotlinx.coroutines.Job? = null
    private var offlineStateJob: kotlinx.coroutines.Job? = null

    init {
        // Load sync config from prefs
        _state.value = _state.value.copy(
            syncConfig = SyncConfig.fromPrefs(prefs)
        )

        // Non-player-dependent — start immediately.
        // flatMapLatest ensures collectors re-bind when the subtitle source is swapped.
        viewModelScope.launch {
            _subtitleSource.flatMapLatest { it.connected }.collect { status ->
                _state.value = _state.value.copy(connectionStatus = status)
            }
        }
        viewModelScope.launch {
            _subtitleSource.flatMapLatest { it.segments }.collect { segs ->
                if (_state.value.playbackMode == PlaybackMode.LIVE_STREAMING) {
                    _state.value = _state.value.copy(segments = segs)
                }
            }
        }
        // Mirror: populate segmentsMeta for the UI (SubtitleList uses it in all modes).
        viewModelScope.launch {
            _subtitleSource.flatMapLatest { it.segmentsMeta }.collect { meta ->
                if (_state.value.playbackMode == PlaybackMode.LIVE_STREAMING) {
                    _state.value = _state.value.copy(segmentsMeta = meta)
                }
            }
        }

        // ── Wait for the player (owned by PlayerService) then start player-dependent flows ──
        viewModelScope.launch {
            val obtained = RadioPlayerHolder.awaitPlayer()
            if (obtained == null) {
                DebugLogger.e(VM, "PlayerService did not start — player unavailable")
                _state.value = _state.value.copy(
                    error = "Media player service failed to start. Please restart the app."
                )
                return@launch
            }
            player = obtained
            DebugLogger.i(VM, "player obtained from RadioPlayerHolder")

            // If user tapped Play before the player was ready, execute it now.
            val pending = pendingPlayUrl
            if (pending != null) {
                pendingPlayUrl = null
                DebugLogger.log(VM, "▶ Executing deferred Play | serverUrl=$pending")
                DebugLogger.i(VM, "executing deferred Play for $pending")
                dispatch(CriAction.Play(pending))
            }

            // Forward playback state (player must be initialised first)
            launch {
                player.playbackState.collect { ps ->
                    if (_state.value.playbackMode == PlaybackMode.LIVE_STREAMING) {
                        _state.value = _state.value.copy(playbackState = ps)
                        if (ps == PlaybackState.PLAYING && coldStartT0 > 0) {
                            DebugLogger.i(TIMING, "event=player_ready elapsed_ms=${(System.nanoTime() - coldStartT0) / 1_000_000}")
                            coldStartT0 = 0 // one-shot
                        }
                    }
                }
            }

            // Forward error messages to the UI error screen
            launch {
                player.lastErrorMessage.collect { msg ->
                    if (_state.value.playbackMode == PlaybackMode.LIVE_STREAMING) {
                        _state.value = _state.value.copy(error = msg)
                    }
                }
            }

            // Main sync loop — subtitle ↔ audio alignment at ~10 Hz
            while (isActive) {
                val isOffline = _state.value.playbackMode == PlaybackMode.OFFLINE_SAVED

                if (isOffline) {
                    // ── Offline mode: lightweight SegmentMeta + lazy SegmentCache ──
                    val segmentsMeta = _state.value.segmentsMeta
                    if (segmentsMeta.isNotEmpty()
                        && _state.value.playbackMode == PlaybackMode.OFFLINE_SAVED) {
                        val engine = SubtitleSyncEngine(segmentsMeta)
                        val activePlayer = offlinePlayer
                        if (activePlayer == null) { delay(100); continue }
                        val playerMs = activePlayer.currentTimelineMs.value
                        val playerSec = playerMs / 1000.0

                        val activeSegmentMeta = engine.findActiveSegment(playerMs)
                        val fullSeg = activeSegmentMeta?.let { segmentCache?.getOrLoad(it.segment_id) }
                        val activeWord = fullSeg?.let { engine.findActiveWord(it, playerMs) }

                        val latestSegment = segmentsMeta.lastOrNull()
                        val delay = if (latestSegment != null && playerMs > 0 && latestSegment.timeline_end_sec > 0) {
                            (playerSec - latestSegment.timeline_end_sec).coerceAtLeast(0.0)
                        } else 0.0

                        // Don't wipe pre-lookup's active segment if sync can't find one
                        val finalSeg = fullSeg ?: _state.value.activeSegment
                        val finalSegId = activeSegmentMeta?.segment_id ?: _state.value.activeSegmentId
                        val finalW = activeWord ?: _state.value.activeWord

                        _state.value = _state.value.copy(
                            activeSegment = finalSeg,
                            activeSegmentId = finalSegId,
                            activeWord = finalW,
                            subtitleDelaySec = delay,
                            lastActiveWord = if (finalW != null) finalW else _state.value.lastActiveWord,
                            offlinePositionMs = {
                                val firstSec = segmentsMeta.firstOrNull()?.timeline_start_sec ?: 0.0
                                if (firstSec > 0 && playerMs > 0) {
                                    (playerMs - (firstSec * 1000).toLong()).coerceAtLeast(0)
                                } else playerMs
                            }(),
                            offlineDurationMs = if (_state.value.offlineDurationMs == 0L) {
                                val first = segmentsMeta.firstOrNull()?.timeline_start_sec ?: 0.0
                                val last = segmentsMeta.lastOrNull()?.timeline_end_sec ?: 0.0
                                if (first > 0 && last > first) ((last - first) * 1000).toLong() else 0L
                            } else _state.value.offlineDurationMs
                        )

                        if (activeSegmentMeta != null && activeSegmentMeta.segment_id != lastActiveSegId) {
                            lastActiveSegId = activeSegmentMeta.segment_id
                            segmentCache?.pin(activeSegmentMeta.segment_id)
                            DebugLogger.i(VM, "▶seg id=${activeSegmentMeta.segment_id} " +
                                "segTL=[${activeSegmentMeta.timeline_start_sec}-${activeSegmentMeta.timeline_end_sec}] " +
                                "playerSec=${"%.1f".format(playerSec)} text=${fullSeg?.text_zh?.take(50) ?: activeSegmentMeta.text_zh.take(50)}")
                        }

                        if (activeWord != null && activeWord !== lastActiveWord) {
                            lastActiveWord = activeWord
                            val relStart = activeWord.start_sec - (activeSegmentMeta.timeline_start_sec)
                            val relEnd = activeWord.end_sec - (activeSegmentMeta.timeline_start_sec)
                            DebugLogger.i(VM, "▷word text=${activeWord.text} " +
                                "wTL=[${activeWord.start_sec}-${activeWord.end_sec}] " +
                                "relTL=[%.3f-%.3f] ".format(relStart, relEnd) +
                                "playerSec=%.3f playerMs=$playerMs".format(playerSec))
                        }

                        val now = System.currentTimeMillis()
                        if (activeSegmentMeta != null && now - lastSyncLog > 2000) {
                            lastSyncLog = now
                            DebugLogger.d(VM, "sync playerSec=%.1f segId=${activeSegmentMeta.segment_id} ".format(playerSec) +
                                "segTL=[${activeSegmentMeta.timeline_start_sec}-${activeSegmentMeta.timeline_end_sec}] " +
                                "word=${activeWord?.text} wTL=[${activeWord?.start_sec}-${activeWord?.end_sec}] " +
                                "delay=${delay.toInt()}s")
                        }

                        if (activeSegmentMeta == null && now - lastSyncLog > 2000) {
                            lastSyncLog = now
                            val first = segmentsMeta.firstOrNull()
                            val last = segmentsMeta.lastOrNull()
                            DebugLogger.w(VM, "sync MISS: no active segment. playerSec=%.1f".format(playerSec) +
                                " playerMs=$playerMs segs=${segmentsMeta.size} " +
                                "loadedRange=[${first?.timeline_start_sec}-${last?.timeline_end_sec}] " +
                                "(player ${if (last != null && playerSec > last.timeline_end_sec) "AHEAD of" else if (first != null && playerSec < first.timeline_start_sec) "BEHIND" else "inside?"} window)")
                        }
                    }
                } else {
                    // ── Live mode: full SubtitleSegment list ──
                    val segments = _state.value.segments
                    if (segments.isNotEmpty()) {
                        val engine = SubtitleSyncEngine(segments.map { seg ->
                            SegmentMeta(
                                segment_id = seg.segment_id,
                                timeline_start_sec = seg.timeline_start_sec,
                                timeline_end_sec = seg.timeline_end_sec,
                                ts_file = seg.ts_file,
                                text_zh = seg.text_zh,
                                text_pinyin = seg.text_pinyin
                            )
                        })
                        val activePlayer = if (::player.isInitialized) player else null
                        if (activePlayer == null) { delay(100); continue }
                        val playerMs = activePlayer.currentTimelineMs.value
                        // Player not READY yet (no timeline) → keep the cold-start
                        // pre-positioned active word instead of wiping it to null.
                        if (playerMs <= 0L) { delay(100); continue }
                        val playerSec = playerMs / 1000.0

                        val activeSegmentMeta = engine.findActiveSegment(playerMs)
                        val activeSegment = activeSegmentMeta?.let { meta ->
                            segments.find { it.segment_id == meta.segment_id }
                        }
                        val activeWord = activeSegment?.let { engine.findActiveWord(it, playerMs) }

                        val latestSegment = segments.lastOrNull()
                        val delay = if (latestSegment != null && playerMs > 0 && latestSegment.timeline_end_sec > 0) {
                            (playerSec - latestSegment.timeline_end_sec).coerceAtLeast(0.0)
                        } else 0.0

                        // Preserve previous segment/word if sync can't find one
                        val finalSegment = activeSegment ?: _state.value.activeSegment
                        val finalSegmentId = activeSegmentMeta?.segment_id ?: _state.value.activeSegmentId
                        val finalWord = activeWord ?: _state.value.activeWord

                        _state.value = _state.value.copy(
                            activeSegment = finalSegment,
                            activeSegmentId = finalSegmentId,
                            activeWord = finalWord,
                            subtitleDelaySec = delay,
                            lastActiveWord = if (finalWord != null) finalWord else _state.value.lastActiveWord,
                            offlinePositionMs = _state.value.offlinePositionMs,
                            offlineDurationMs = _state.value.offlineDurationMs
                        )

                        if (finalSegment != null && finalSegment.segment_id != lastActiveSegId) {
                            lastActiveSegId = finalSegment.segment_id
                            DebugLogger.i(VM, "▶seg id=${finalSegment.segment_id} " +
                                "segTL=[${finalSegment.timeline_start_sec}-${finalSegment.timeline_end_sec}] " +
                                "playerSec=${"%.1f".format(playerSec)} text=${finalSegment.text_zh.take(50)}")
                        }

                        if (activeWord != null && activeWord !== lastActiveWord) {
                            lastActiveWord = activeWord
                            val relStart = activeWord.start_sec - (activeSegment.timeline_start_sec)
                            val relEnd = activeWord.end_sec - (activeSegment.timeline_start_sec)
                            DebugLogger.i(VM, "▷word text=${activeWord.text} " +
                                "wTL=[${activeWord.start_sec}-${activeWord.end_sec}] " +
                                "relTL=[%.3f-%.3f] ".format(relStart, relEnd) +
                                "playerSec=%.3f playerMs=$playerMs".format(playerSec))
                        }

                        val now = System.currentTimeMillis()
                        if (finalSegment != null && now - lastSyncLog > 2000) {
                            lastSyncLog = now
                            DebugLogger.d(VM, "sync playerSec=%.1f segId=${finalSegment.segment_id} ".format(playerSec) +
                                "segTL=[${finalSegment.timeline_start_sec}-${finalSegment.timeline_end_sec}] " +
                                "word=${finalWord?.text} wTL=[${finalWord?.start_sec}-${finalWord?.end_sec}] " +
                                "delay=${delay.toInt()}s")
                        }

                        if (finalSegment == null && now - lastSyncLog > 2000) {
                            lastSyncLog = now
                            val first = segments.firstOrNull()
                            val last = segments.lastOrNull()
                            DebugLogger.w(VM, "sync MISS: no active segment. playerSec=%.1f".format(playerSec) +
                                " playerMs=$playerMs segs=${segments.size} " +
                                "loadedRange=[${first?.timeline_start_sec}-${last?.timeline_end_sec}] " +
                                "(player ${if (last != null && playerSec > last.timeline_end_sec) "AHEAD of" else if (first != null && playerSec < first.timeline_start_sec) "BEHIND" else "inside?"} window)")
                        }

                        // Audio starts at (live edge − target offset) and stays there;
                        // subtitles are in lockstep, so no runtime delay-seek is needed.
                    }
                }
                delay(100)
            }
        }
    }

    private fun requirePlayer(): Boolean = ::player.isInitialized

    /** Returns the player active for the current [PlaybackMode]. */
    private fun activePlayerOrNull(): RadioPlayer? {
        return if (_state.value.playbackMode == PlaybackMode.OFFLINE_SAVED) {
            offlinePlayer
        } else {
            if (::player.isInitialized) player else null
        }
    }

    /** Like [activePlayerOrNull] but logs and returns false if no player is available. */
    private fun requireActivePlayer(): Boolean {
        val p = activePlayerOrNull()
        if (p == null) {
            DebugLogger.w(VM, "requireActivePlayer — no player for mode ${_state.value.playbackMode}")
        }
        return p != null
    }

    fun dispatch(action: CriAction) {
        when (action) {
            is CriAction.Play -> {
                DebugLogger.log(VM, "▶ Play tapped | serverUrl=${action.serverUrl} | mode=${_state.value.playbackMode}")
                _state.value = _state.value.copy(error = null)
                when (_state.value.playbackMode) {
                    PlaybackMode.LIVE_STREAMING -> {
                        if (!requirePlayer()) {
                            // PlayerService hasn't bound yet — defer Play until it arrives.
                            DebugLogger.log(VM, "⏳ Play deferred — player not ready, will auto-play when available")
                            pendingPlayUrl = action.serverUrl
                            _state.value = _state.value.copy(playbackState = PlaybackState.LOADING)
                            return
                        }
                        DebugLogger.i(VM, "play server=${action.serverUrl}")
                        val url = "${action.serverUrl}/hls/playlist.m3u8"
                        DebugLogger.log(VM, "HLS URL = $url")
                        val wasPaused = _state.value.playbackState == PlaybackState.PAUSED
                        if (wasPaused && action.serverUrl == currentServerUrl) {
                            DebugLogger.i(VM, "play resuming from paused position")
                            DebugLogger.log(VM, "▶ Play resume | serverUrl=${action.serverUrl}")
                            player.resume()
                        } else {
                            DebugLogger.i(VM, "play new stream")
                            currentServerUrl = action.serverUrl
                            coldStartT0 = System.nanoTime()
                            DebugLogger.i(TIMING, "event=play_tapped elapsed_ms=0")
                            DebugLogger.log(VM, "▶ Play cold-start | serverUrl=${action.serverUrl} | protocol=${_state.value.metadataProtocol}")

                            if (subtitleSource is com.crimobile.subtitles.HttpSubtitleSource) {
                                _state.value = _state.value.copy(playbackState = PlaybackState.LOADING)
                                viewModelScope.launch {
                                    val http = subtitleSource as com.crimobile.subtitles.HttpSubtitleSource

                                    DebugLogger.i(TIMING, "event=fetch_initial_start elapsed_ms=${(System.nanoTime() - coldStartT0) / 1_000_000}")
                                    DebugLogger.log(VM, "→ fetchInitial(server=${action.serverUrl}, n=$INITIAL_BATCH, lite=true)")

                                    // fetchInitial with retry (up to 3 attempts)
                                    var ok = false
                                    for (attempt in 1..3) {
                                        try {
                                            ok = http.fetchInitial(action.serverUrl, INITIAL_BATCH, lite = true)
                                            if (ok) break
                                            DebugLogger.log(VM, "← fetchInitial attempt $attempt returned false")
                                            if (attempt < 3) delay(2000)
                                        } catch (e: Exception) {
                                            DebugLogger.log(VM, "✗ fetchInitial attempt $attempt FAILED", e)
                                            if (attempt == 3) {
                                                _state.value = _state.value.copy(
                                                    error = "Cannot reach server.\n${e.message}",
                                                    playbackState = PlaybackState.IDLE
                                                )
                                                return@launch
                                            }
                                            delay(2000)
                                        }
                                    }

                                    if (!ok) {
                                        DebugLogger.log(VM, "✗ fetchInitial failed after 3 attempts")
                                        _state.value = _state.value.copy(
                                            error = "Cannot reach server.\nCheck your connection and try again.",
                                            playbackState = PlaybackState.IDLE
                                        )
                                        return@launch
                                    }

                                    DebugLogger.i(TIMING, "event=fetch_initial_done ok=$ok elapsed_ms=${(System.nanoTime() - coldStartT0) / 1_000_000}")
                                    DebugLogger.log(VM, "← fetchInitial ok | elapsed=${(System.nanoTime() - coldStartT0) / 1_000_000}ms")

                                    // Pre-lookup removed: computing playerSec from metadata timeline
                                    // is unreliable (30s+ mismatch with HLS playlist position on cold start
                                    // where segments have gaps). The 10Hz sync loop finds the active
                                    // segment within 1-2 frames (~100-200ms) — imperceptible.

                                    player.play(url)
                                    DebugLogger.i(TIMING, "event=player_play_called elapsed_ms=${(System.nanoTime() - coldStartT0) / 1_000_000}")
                                    DebugLogger.log(VM, "→ player.play(url) called | elapsed=${(System.nanoTime() - coldStartT0) / 1_000_000}ms")
                                    try {
                                        http.connect(action.serverUrl)
                                        DebugLogger.log(VM, "→ http.connect() OK")
                                    } catch (e: Exception) {
                                        DebugLogger.log(VM, "✗ http.connect() FAILED", e)
                                    }
                                }
                            } else {
                                DebugLogger.log(VM, "→ SSE source: connect + play")
                                subtitleSource.connect(action.serverUrl)
                                player.play(url)
                            }
                        }
                    }
                    PlaybackMode.OFFLINE_SAVED -> {
                        val op = offlinePlayer
                        if (op == null) {
                            DebugLogger.w(VM, "play offline — no offline player")
                            DebugLogger.log(VM, "✗ Play offline aborted — offlinePlayer is null")
                            return
                        }
                        DebugLogger.i(VM, "play offline")
                        DebugLogger.log(VM, "▶ Play offline | segments=${_state.value.segmentsMeta.size}")
                        op.play("")
                    }
                }
                _state.value = _state.value.copy(isPronouncing = false)
            }
            CriAction.Pause -> {
                val ap = activePlayerOrNull() ?: return
                DebugLogger.i(VM, "pause")
                ap.pause()
                _state.value = _state.value.copy(isPronouncing = false)
            }
            CriAction.Resume -> {
                val ap = activePlayerOrNull() ?: return
                // Offline player always has content loaded; just resume.
                // Live player needs a stream URL or falls back to a full Play.
                if (_state.value.playbackMode == PlaybackMode.LIVE_STREAMING && currentServerUrl.isEmpty()) {
                    dispatch(CriAction.Play(ServerConfig.defaultUrl))
                    return
                }
                DebugLogger.i(VM, "resume")
                ap.resume()
                _state.value = _state.value.copy(isPronouncing = false)
            }
            is CriAction.WordTapped -> {
                val ap = activePlayerOrNull() ?: return
                DebugLogger.i(VM, "word_tapped text=${action.word.text} pinyin=${action.word.pinyin}")
                ap.pause()
                val segment = segmentCache?.getOrLoad(action.segmentId)
                    ?: _state.value.segments.find { it.segment_id == action.segmentId }
                val timelineMs = (action.word.start_sec * 1000).toLong()

                val currentActive = _state.value.activeWord
                if (currentActive != action.word) {
                    ap.seekTo(timelineMs)
                }

                _state.value = _state.value.copy(
                    wordPopup = WordPopupState(
                        word = action.word,
                        segment = segment ?: return,
                        pinyin = action.word.pinyin,
                        translation = action.word.translation,
                        senses = action.word.senses,
                        cedictMeanings = action.word.cedict_meanings
                    )
                )
                savedWord.value = action.word

                // Lazy dictionary fetch: if the word was cold-loaded via ?lite=true,
                // its translation/senses/cedict_meanings are empty. Fetch the full
                // segment once and update the popup + cache so subsequent taps on
                // any word in this segment use the cached full data.
                val needsDict = action.word.translation.isEmpty() &&
                    action.word.senses.isEmpty() &&
                    action.word.cedict_meanings.isEmpty()
                if (needsDict && segment != null && segment.segment_id !in pendingDictFetches) {
                    pendingDictFetches.add(segment.segment_id)
                    viewModelScope.launch {
                        try {
                            val fullSeg = subtitleSource.fetchSegmentFull(
                                currentServerUrl, segment.segment_id
                            )
                            if (fullSeg != null) {
                                // Cache the full segment so subsequent taps skip the network.
                                subtitleSource.upsertSegment(fullSeg)

                                // Find the matching word in the full segment.
                                val fullWord = fullSeg.words.find { w ->
                                    w.text == action.word.text &&
                                        w.char_start == action.word.char_start
                                }
                                // Update the popup if it still shows the same word.
                                val currentPopup = _state.value.wordPopup
                                if (fullWord != null) {
                                    savedWord.value = fullWord
                                }
                                if (currentPopup != null &&
                                    currentPopup.word.text == action.word.text &&
                                    currentPopup.word.char_start == action.word.char_start
                                ) {
                                    _state.value = _state.value.copy(
                                        wordPopup = currentPopup.copy(
                                            word = fullWord ?: currentPopup.word,
                                            segment = fullSeg,
                                            translation = fullWord?.translation ?: "",
                                            senses = fullWord?.senses ?: emptyList(),
                                            cedictMeanings = fullWord?.cedict_meanings ?: emptyList()
                                        )
                                    )
                                }
                                DebugLogger.i(VM, "dict_lazy_fetched seg=${segment.segment_id} " +
                                    "word=${action.word.text} trans=${fullWord?.translation?.take(30)}")
                            }
                        } catch (e: Exception) {
                            DebugLogger.w(VM, "dict_lazy_fetch failed seg=${segment.segment_id}: ${e.message}")
                        } finally {
                            pendingDictFetches.remove(segment.segment_id)
                        }
                    }
                }
            }
            CriAction.DismissPopup -> {
                _state.value = _state.value.copy(wordPopup = null, isPronouncing = false)
            }
            CriAction.PronounceWord -> {
                DebugLogger.i(VM, "pronounce_word")
                val word = savedWord.value ?: return
                val words = _state.value.activeSegment?.words ?: return
                val wordIdx = words.indexOfFirst { w -> w === word }
                if (wordIdx < 0) {
                    pronunciationPlayer.playWord(word)
                } else {
                    val prevTimeTo = if (wordIdx > 0) words[wordIdx - 1].end_sec else null
                    val nextTimeFrom = if (wordIdx < words.size - 1) words[wordIdx + 1].start_sec else null
                    pronunciationPlayer.playWord(word, prevTimeTo, nextTimeFrom)
                }
                _state.value = _state.value.copy(isPronouncing = true)
            }
            CriAction.SaveWord -> {
                DebugLogger.i(VM, "save_word")
                val word = savedWord.value ?: return
                val context = _state.value.activeSegment?.text_zh ?: ""
                vocabularyStore.appendWord(word, context)
            }
            CriAction.TogglePinyin -> {
                val newVal = !_state.value.showPinyin
                _state.value = _state.value.copy(showPinyin = newVal)
                prefs.edit().putBoolean("show_pinyin", newVal).apply()
            }
            is CriAction.SetFontSize -> {
                _state.value = _state.value.copy(fontSizeSp = action.sp)
                prefs.edit().putInt("font_size_sp", action.sp).apply()
            }
            CriAction.ToggleWordBoundaries -> {
                val newVal = !_state.value.showWordBoundaries
                _state.value = _state.value.copy(showWordBoundaries = newVal)
                prefs.edit().putBoolean("show_word_boundaries", newVal).apply()
            }
            CriAction.ToggleAudioBoundaries -> {
                val newVal = !_state.value.showAudioBoundaries
                _state.value = _state.value.copy(showAudioBoundaries = newVal)
                prefs.edit().putBoolean("show_audio_boundaries", newVal).apply()
            }
            is CriAction.SetPinyinFontSize -> {
                _state.value = _state.value.copy(pinyinFontSizeSp = action.sp)
                prefs.edit().putInt("pinyin_font_size_sp", action.sp).apply()
            }
            is CriAction.SetDictFontSize -> {
                _state.value = _state.value.copy(dictFontSizeSp = action.sp)
                prefs.edit().putInt("dict_font_size_sp", action.sp).apply()
            }
            CriAction.EnableDebug -> {
                _state.value = _state.value.copy(debugEnabled = true)
                prefs.edit().putBoolean("debug_enabled", true).apply()
            }
            CriAction.ToggleLogToFile -> {
                val newVal = !_state.value.logToFileEnabled
                com.crimobile.debug.DebugLogger.enabled = newVal
                _state.value = _state.value.copy(logToFileEnabled = newVal)
                prefs.edit().putBoolean("log_to_file_enabled", newVal).apply()
                DebugLogger.i(VM, "logToFile = $newVal")
            }
            is CriAction.SetPlaybackMode -> {
                switchPlaybackMode(action.mode)
            }
            is CriAction.UpdateSyncConfig -> {
                val cfg = action.config
                _state.value = _state.value.copy(syncConfig = cfg)
                SyncConfig.save(prefs, cfg)
                SyncScheduler.schedule(getApplication(), cfg)
            }
            CriAction.LoadArchiveInfo -> {
                viewModelScope.launch {
                    try {
                        val engine = DownloadEngine(
                            getApplication(),
                            ServerConfig.defaultUrl,
                            offlineStorageManager
                        )
                        val info = engine.fetchArchiveInfo()
                        _state.value = _state.value.copy(archiveInfo = info)
                    } catch (e: Exception) {
                        DebugLogger.w(VM, "Failed to load archive info: ${e.message}")
                    }
                }
            }
            CriAction.StartInitialSync -> {
                startDownload()
            }
            CriAction.CancelDownload -> {
                downloadJob?.cancel()
                downloadJob = null
                _state.value = _state.value.copy(
                    downloadProgress = DownloadProgress(isRunning = false, error = "Cancelled")
                )
            }
            CriAction.OpenOfflineNavDialog -> {
                val sessions = offlineStorageManager.loadAllSessions().map { s ->
                    OfflineSessionInfo(
                        sessionId = offlineStorageManager.sessionId(s.startSec, s.durationSec),
                        startSec = s.startSec,
                        durationSec = s.durationSec,
                        segmentCount = s.segmentCount,
                        createdAt = s.createdAt
                    )
                }.sortedByDescending { it.createdAt }
                _state.value = _state.value.copy(
                    showOfflineNavDialog = true,
                    offlineSessions = sessions,
                    offlineSessionSegments = emptyList(),
                    selectedOfflineSessionId = null
                )
            }
            CriAction.DismissOfflineNavDialog -> {
                _state.value = _state.value.copy(showOfflineNavDialog = false)
            }
            is CriAction.SelectOfflineSession -> {
                _state.value = _state.value.copy(selectedOfflineSessionId = action.sessionId)
                viewModelScope.launch(Dispatchers.IO) {
                    val segs = offlineStorageManager.loadSegmentsForSession(action.sessionId)
                    withContext(Dispatchers.Main) {
                        _state.value = _state.value.copy(offlineSessionSegments = segs)
                        // Rebuild player with new session's segments
                        if (segs.isNotEmpty()) {
                            offlineStateJob?.cancel()
                            offlinePlayer?.release()
                            offlinePlayer = OfflineRadioPlayer(
                                segs,
                                offlineStorageManager,
                                action.sessionId,
                                getApplication()
                            )
                            offlinePlayer?.pause()
                            segmentCache?.clear()
                            segmentCache = SegmentCache(offlineStorageManager, action.sessionId)
                            val op = offlinePlayer!!
                            offlineStateJob = viewModelScope.launch {
                                op.playbackState.collect { ps ->
                                    if (_state.value.playbackMode == PlaybackMode.OFFLINE_SAVED) {
                                        _state.value = _state.value.copy(playbackState = ps)
                                    }
                                }
                            }
                            _state.value = _state.value.copy(
                                segmentsMeta = segs,
                                segments = emptyList()
                            )
                        }
                    }
                }
            }
            is CriAction.SelectOfflineSegment -> {
                val seg = _state.value.offlineSessionSegments.find { it.segment_id == action.segmentId }
                    ?: return
                offlinePlayer?.seekTo((seg.timeline_start_sec * 1000).toLong())
                offlinePlayer?.resume()
                _state.value = _state.value.copy(
                    showOfflineNavDialog = false,
                    error = null
                )
            }
            is CriAction.SetMetadataProtocol -> {
                val newProtocol = action.protocol
                if (newProtocol == _state.value.metadataProtocol) return
                DebugLogger.i(VM, "SetMetadataProtocol → $newProtocol")

                // Persist preference
                prefs.edit().putString("metadata_protocol", newProtocol).apply()

                // Disconnect old source
                val oldSource = _subtitleSource.value
                oldSource.disconnect()

                // Build new source
                val newSource = if (newProtocol == "SSE") SseSubtitleSource() else HttpSubtitleSource()
                _subtitleSource.value = newSource

                // Re-connect if a live stream is active
                if (_state.value.playbackMode == PlaybackMode.LIVE_STREAMING && currentServerUrl.isNotEmpty()) {
                    newSource.connect(currentServerUrl)
                }

                _state.value = _state.value.copy(metadataProtocol = newProtocol)
            }
        }
    }

    // ── Offline mode helpers ───────────────────────────────────────────

    private fun switchPlaybackMode(mode: PlaybackMode) {
        if (mode == _state.value.playbackMode) return
        DebugLogger.i(VM, "switchPlaybackMode → $mode")

        when (mode) {
            PlaybackMode.LIVE_STREAMING -> {
                // Tear down offline
                offlineStateJob?.cancel()
                offlineStateJob = null
                offlinePlayer?.release()
                offlinePlayer = null
                // Restart live stream: reconnect SSE + player, but leave paused
                if (::player.isInitialized && currentServerUrl.isNotEmpty()) {
                    val hlsUrl = "$currentServerUrl/hls/playlist.m3u8"
                    subtitleSource.connect(currentServerUrl)
                    player.play(hlsUrl)
                    player.pause()
                }
                // Clear stale offline subtitle state. Otherwise the sync loop would
                // briefly compute delay = livePlayerSec − lastOfflineSegment.end
                // (the age of the archive, e.g. ~2951s) and flash a bogus lag until
                // the live source repopulates. Empty segments ⇒ delay = 0.
                segmentCache?.clear()
                segmentCache = null
                lastActiveWord = null
                _state.value = _state.value.copy(
                    playbackMode = mode,
                    playbackState = if (::player.isInitialized) player.playbackState.value else PlaybackState.IDLE,
                    segments = emptyList(),
                    segmentsMeta = emptyList(),
                    activeSegment = null,
                    activeSegmentId = null,
                    activeWord = null,
                    lastActiveWord = null,
                    subtitleDelaySec = 0.0,
                    error = null
                )
            }
            PlaybackMode.OFFLINE_SAVED -> {
                // Disconnect live SSE and pause player — only if a stream was active
                if (currentServerUrl.isNotEmpty()) {
                    subtitleSource.disconnect()
                    if (::player.isInitialized) {
                        player.pause()
                    }
                }

                // Flip the UI to offline IMMEDIATELY (instant switch). The heavy disk
                // read — parsing every stored segment JSON — used to run inline on the
                // main thread and froze the UI for ~seconds. It now runs off-thread
                // below. playbackState=LOADING keeps the setup screen from flashing and
                // shows LoadingScreen while segments are read.
                segmentCache?.clear()
                segmentCache = null
                lastActiveWord = null
                _state.value = _state.value.copy(
                    playbackMode = mode,
                    playbackState = PlaybackState.LOADING,
                    segments = emptyList(),
                    segmentsMeta = emptyList(),
                    activeSegment = null,
                    activeSegmentId = null,
                    activeWord = null,
                    lastActiveWord = null,
                    subtitleDelaySec = 0.0,
                    offlinePositionMs = 0L,
                    error = null
                )

                offlineStateJob?.cancel()
                offlineStateJob = viewModelScope.launch {
                    // Heavy disk read off the main thread.
                    withContext(Dispatchers.IO) {
                        offlineSubtitleSource.load()
                    }
                    val meta = offlineSubtitleSource.segmentsMeta.value
                    segmentCache = offlineSubtitleSource.segmentCache

                    // User may have switched back to live while loading.
                    if (_state.value.playbackMode != PlaybackMode.OFFLINE_SAVED) return@launch

                    // ExoPlayer must be constructed on the main thread (we are here).
                    if (meta.isNotEmpty()) {
                        offlinePlayer?.release()
                        offlinePlayer = OfflineRadioPlayer(
                            meta,
                            offlineStorageManager,
                            offlineSubtitleSource.lastLoadedSessionId ?: "0_0",
                            getApplication()
                        )
                        offlinePlayer?.pause()
                    }
                    val durationMs = if (meta.isNotEmpty()) {
                        val first = meta.first().timeline_start_sec
                        val last = meta.last().timeline_end_sec
                        if (last > first) ((last - first) * 1000).toLong() else 0L
                    } else 0L
                    _state.value = _state.value.copy(
                        segmentsMeta = meta,
                        segments = emptyList(),
                        offlineDurationMs = durationMs,
                        playbackState = offlinePlayer?.playbackState?.value ?: PlaybackState.IDLE
                    )

                    // Collect offline player state so the Play/Pause button responds.
                    val op = offlinePlayer
                    if (op != null) {
                        launch {
                            op.playbackState.collect { ps ->
                                if (_state.value.playbackMode == PlaybackMode.OFFLINE_SAVED) {
                                    _state.value = _state.value.copy(playbackState = ps)
                                }
                            }
                        }
                    }

                    // Local range is the heaviest read (re-scans all sessions) and the
                    // least urgent — compute it last, off-thread, publish when ready.
                    val range = withContext(Dispatchers.IO) { offlineStorageManager.computeLocalRange() }
                    if (_state.value.playbackMode == PlaybackMode.OFFLINE_SAVED) {
                        _state.value = _state.value.copy(offlineLocalRangeSec = range)
                    }
                }
            }
        }
    }

    private fun startDownload() {
        val cfg = _state.value.syncConfig
        downloadJob?.cancel()
        downloadJob = viewModelScope.launch {
            val engine = DownloadEngine(
                getApplication(),
                ServerConfig.defaultUrl,
                offlineStorageManager
            )

            // Fetch archive info for bounds validation
            val archive = try {
                engine.fetchArchiveInfo()
            } catch (e: Exception) {
                _state.value = _state.value.copy(
                    downloadProgress = DownloadProgress(error = "Cannot reach server: ${e.message}")
                )
                return@launch
            }
            _state.value = _state.value.copy(archiveInfo = archive)

            // Download window ends at current time. The server naturally
            // limits results to only segments that exist in the index.
            val nowSec = System.currentTimeMillis() / 1000.0
            var startSec = nowSec - cfg.syncDurationSec
            val endSec = nowSec

            // Clamp start to archive bounds (only when server reports valid bounds)
            if (archive.oldestStartSec > 0.0 && startSec < archive.oldestStartSec) {
                startSec = archive.oldestStartSec
            }

            // Run download
            val result = engine.downloadRange(startSec, endSec) { progress ->
                _state.value = _state.value.copy(downloadProgress = progress)
            }

            if (result.isSuccess) {
                // Prune old sessions before marking sync done
                offlineStorageManager.pruneOldSessions(cfg.keepLastNSyncs)

                // Mark initial sync done
                val updatedConfig = cfg.copy(
                    lastSyncTimestamp = System.currentTimeMillis(),
                    initialSyncDone = true
                )
                _state.value = _state.value.copy(syncConfig = updatedConfig)
                SyncConfig.save(prefs, updatedConfig)
                SyncScheduler.schedule(getApplication(), updatedConfig)

                // If in offline mode, reload segments
                if (_state.value.playbackMode == PlaybackMode.OFFLINE_SAVED) {
                    offlineSubtitleSource.load()
                    val meta = offlineSubtitleSource.segmentsMeta.value
                    segmentCache = offlineSubtitleSource.segmentCache
                    _state.value = _state.value.copy(segmentsMeta = meta, segments = emptyList())
                    if (meta.isNotEmpty()) {
                        val durationMs = if (meta.isNotEmpty()) {
                            val first = meta.first().timeline_start_sec
                            val last = meta.last().timeline_end_sec
                            if (last > first) ((last - first) * 1000).toLong() else 0L
                        } else 0L
                        offlinePlayer?.release()
                        offlinePlayer = OfflineRadioPlayer(
                            meta,
                            offlineStorageManager,
                            offlineSubtitleSource.lastLoadedSessionId ?: "0_0",
                            getApplication()
                        )
                        offlinePlayer?.pause()
                        _state.value = _state.value.copy(
                            segmentsMeta = meta,
                            segments = emptyList(),
                            offlineDurationMs = durationMs,
                            playbackState = offlinePlayer?.playbackState?.value ?: PlaybackState.IDLE
                        )
                    }
                }
            }
        }
    }

    companion object {
        private const val VM = "CRIRadio:vm"
        private const val TIMING = "CRIRadio:timing"
        /** Segments fetched in the cold-start batch (word timing + pinyin, no dict). */
        private const val INITIAL_BATCH = 40
    }

    override fun onCleared() {
        super.onCleared()
        // pronunciationPlayer is lazy — its init may fail if the live player was
        // never created (lateinit not initialized). Safe-call via runCatching.
        runCatching { pronunciationPlayer.stop() }
        subtitleSource.disconnect()
        downloadJob?.cancel()
        offlinePlayer?.release()
        segmentCache?.clear()
        segmentCache = null
        // Live player is owned by PlayerService — do NOT release it here.
        // The service survives Activity destruction and keeps audio alive.
    }
}

package com.crimobile.viewmodel

import android.app.Application
import android.content.Context
import android.util.Log
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.crimobile.ServerConfig
import com.crimobile.model.*
import com.crimobile.offline.DownloadEngine
import com.crimobile.offline.DownloadProgress
import com.crimobile.offline.OfflineRadioPlayer
import com.crimobile.offline.OfflineStorageManager
import com.crimobile.offline.OfflineSubtitleSource
import com.crimobile.offline.SyncConfig
import com.crimobile.offline.SyncScheduler
import com.crimobile.player.RadioPlayer
import com.crimobile.player.RadioPlayerHolder
import com.crimobile.pronounce.PronunciationPlayer
import com.crimobile.subtitles.SseSubtitleSource
import com.crimobile.subtitles.SubtitleSource
import com.crimobile.sync.SubtitleSyncEngine
import com.crimobile.vocabulary.VocabularyStore
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

data class CriViewState(
    val playbackState: PlaybackState = PlaybackState.IDLE,
    val segments: List<SubtitleSegment> = emptyList(),
    val activeWord: WordEntry? = null,
    val activeSegment: SubtitleSegment? = null,
    val showPinyin: Boolean = false,
    val fontSizeSp: Int = 22,  // subtitle font size in sp
    val showWordBoundaries: Boolean = false,  // subtle underline under words
    val showAudioBoundaries: Boolean = false,  // debug: show .ts file boundaries
    val pinyinFontSizeSp: Int = 9,  // pinyin font size in sp
    val debugEnabled: Boolean = false,  // true when .cri_debug file exists
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
    val offlineLocalRangeSec: Pair<Double, Double>? = null  // (oldest, newest) of downloaded segments in epoch seconds
)

sealed class CriAction {
    data class Play(val serverUrl: String) : CriAction()
    object Pause : CriAction()
    object Resume : CriAction()
    data class WordTapped(val word: WordEntry) : CriAction()
    object DismissPopup : CriAction()
    object PronounceWord : CriAction()
    object SaveWord : CriAction()
    object TogglePinyin : CriAction()
    data class SetFontSize(val sp: Int) : CriAction()
    object ToggleWordBoundaries : CriAction()
    object ToggleAudioBoundaries : CriAction()
    data class SetPinyinFontSize(val sp: Int) : CriAction()
    object EnableDebug : CriAction()
    data class SetPlaybackMode(val mode: PlaybackMode) : CriAction()
    data class UpdateSyncConfig(val config: SyncConfig) : CriAction()
    object LoadArchiveInfo : CriAction()
    object StartInitialSync : CriAction()
    object CancelDownload : CriAction()
}

class CriViewModel(application: Application) : AndroidViewModel(application) {

    // Player is now owned by PlayerService (foreground service).
    // We obtain it via the singleton holder — same StateFlow, zero IPC latency.
    private lateinit var player: RadioPlayer

    private val subtitleSource: SubtitleSource = SseSubtitleSource()
    private val vocabularyStore = VocabularyStore(application)
    private val pronunciationPlayer by lazy { PronunciationPlayer(player, viewModelScope) }

    private val prefs = application.getSharedPreferences("cri_prefs", Context.MODE_PRIVATE)

    private val _state = MutableStateFlow(
        CriViewState(
            showPinyin = prefs.getBoolean("show_pinyin", false),
            fontSizeSp = prefs.getInt("font_size_sp", 22),
            showWordBoundaries = prefs.getBoolean("show_word_boundaries", false),
            showAudioBoundaries = prefs.getBoolean("show_audio_boundaries", false),
            pinyinFontSizeSp = prefs.getInt("pinyin_font_size_sp", 9),
            debugEnabled = prefs.getBoolean("debug_enabled", false),
        )
    )
    val state: StateFlow<CriViewState> = _state.asStateFlow()

    private val savedWord = MutableStateFlow<WordEntry?>(null)
    private var currentServerUrl: String = ""
    private var lastSyncLog = 0L
    private var lastActiveSegId = -1
    private var lastActiveWord: WordEntry? = null
    private var initialDelaySeekDone = false  // one-shot seek behind live edge after connect

    // ── Offline mode ───────────────────────────────────────────────────
    private val offlineStorageManager by lazy { OfflineStorageManager(getApplication()) }
    private val offlineSubtitleSource by lazy { OfflineSubtitleSource(offlineStorageManager) }
    private var offlinePlayer: OfflineRadioPlayer? = null
    private var downloadJob: kotlinx.coroutines.Job? = null
    private var offlineStateJob: kotlinx.coroutines.Job? = null

    init {
        // Load sync config from prefs
        _state.value = _state.value.copy(
            syncConfig = SyncConfig.fromPrefs(prefs)
        )

        // Non-player-dependent — start immediately
        viewModelScope.launch {
            subtitleSource.connected.collect { status ->
                _state.value = _state.value.copy(connectionStatus = status)
            }
        }
        viewModelScope.launch {
            subtitleSource.segments.collect { segs ->
                if (_state.value.playbackMode == PlaybackMode.LIVE_STREAMING) {
                    _state.value = _state.value.copy(segments = segs)
                }
            }
        }

        // ── Wait for the player (owned by PlayerService) then start player-dependent flows ──
        viewModelScope.launch {
            val obtained = RadioPlayerHolder.awaitPlayer()
            if (obtained == null) {
                Log.e(VM, "PlayerService did not start — player unavailable")
                _state.value = _state.value.copy(
                    error = "Media player service failed to start. Please restart the app."
                )
                return@launch
            }
            player = obtained
            Log.i(VM, "player obtained from RadioPlayerHolder")

            // Forward playback state (player must be initialised first)
            launch {
                player.playbackState.collect { ps ->
                    if (_state.value.playbackMode == PlaybackMode.LIVE_STREAMING) {
                        _state.value = _state.value.copy(playbackState = ps)
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
                // Read segments from state (works for both live and offline modes)
                val segments = _state.value.segments
                if (segments.isNotEmpty()) {
                    val engine = SubtitleSyncEngine(segments)
                    // Use correct player based on mode
                    val activePlayer = if (_state.value.playbackMode == PlaybackMode.OFFLINE_SAVED) {
                        offlinePlayer
                    } else {
                        if (::player.isInitialized) player else null
                    }
                    if (activePlayer == null) { delay(100); continue }
                    val playerMs = activePlayer.currentTimelineMs.value
                    val playerSec = playerMs / 1000.0

                    val activeSegment = engine.findActiveSegment(playerMs)
                    val activeWord = activeSegment?.let { engine.findActiveWord(it, playerMs) }

                    val latestSegment = segments.lastOrNull()
                    val delay = if (latestSegment != null && playerMs > 0 && latestSegment.timeline_end_sec > 0) {
                        (playerSec - latestSegment.timeline_end_sec).coerceAtLeast(0.0)
                    } else 0.0

                    _state.value = _state.value.copy(
                        activeSegment = activeSegment,
                        activeWord = activeWord,
                        subtitleDelaySec = delay,
                        lastActiveWord = if (activeWord != null) activeWord else _state.value.lastActiveWord,
                        offlinePositionMs = if (_state.value.playbackMode == PlaybackMode.OFFLINE_SAVED) {
                            val firstSec = segments.firstOrNull()?.timeline_start_sec ?: 0.0
                            if (firstSec > 0 && playerMs > 0) {
                                (playerMs - (firstSec * 1000).toLong()).coerceAtLeast(0)
                            } else playerMs
                        } else _state.value.offlinePositionMs,
                        offlineDurationMs = if (_state.value.playbackMode == PlaybackMode.OFFLINE_SAVED && _state.value.offlineDurationMs == 0L) {
                            val first = segments.firstOrNull()?.timeline_start_sec ?: 0.0
                            val last = segments.lastOrNull()?.timeline_end_sec ?: 0.0
                            if (first > 0 && last > first) ((last - first) * 1000).toLong() else 0L
                        } else _state.value.offlineDurationMs
                    )

                    if (activeSegment != null && activeSegment.segment_id != lastActiveSegId) {
                        lastActiveSegId = activeSegment.segment_id
                        Log.i(VM, "▶seg id=${activeSegment.segment_id} " +
                            "segTL=[${activeSegment.timeline_start_sec}-${activeSegment.timeline_end_sec}] " +
                            "playerSec=${"%.1f".format(playerSec)} text=${activeSegment.text_zh.take(50)}")
                    }

                    if (activeWord != null && activeWord !== lastActiveWord) {
                        lastActiveWord = activeWord
                        val relStart = activeWord.start_sec - (activeSegment?.timeline_start_sec ?: 0.0)
                        val relEnd = activeWord.end_sec - (activeSegment?.timeline_start_sec ?: 0.0)
                        Log.i(VM, "▷word text=${activeWord.text} " +
                            "wTL=[${activeWord.start_sec}-${activeWord.end_sec}] " +
                            "relTL=[%.3f-%.3f] ".format(relStart, relEnd) +
                            "playerSec=%.3f playerMs=$playerMs".format(playerSec))
                    }

                    val now = System.currentTimeMillis()
                    if (activeSegment != null && now - lastSyncLog > 2000) {
                        lastSyncLog = now
                        Log.d(VM, "sync playerSec=%.1f segId=${activeSegment.segment_id} ".format(playerSec) +
                            "segTL=[${activeSegment.timeline_start_sec}-${activeSegment.timeline_end_sec}] " +
                            "word=${activeWord?.text} wTL=[${activeWord?.start_sec}-${activeWord?.end_sec}] " +
                            "delay=${delay.toInt()}s")
                    }

                    // ── One-shot delay seek: rewind player behind live edge ──
                    // Only for LIVE_STREAMING mode — offline content has no live edge.
                    if (_state.value.playbackMode == PlaybackMode.LIVE_STREAMING &&
                        !initialDelaySeekDone && playerMs > 0 && segments.size >= MIN_BUFFER_FOR_DELAY_SEEK) {
                        val newest = segments.last().timeline_start_sec
                        val oldest = segments.first().timeline_start_sec
                        val availableSec = newest - oldest
                        if (availableSec > 5.0) {
                            val targetDelay = minOf(DELAY_TARGET_SEC.toDouble(), availableSec * 0.8)
                            val seekTargetMs = ((newest - targetDelay) * 1000).toLong()
                            player.seekTo(seekTargetMs)
                            initialDelaySeekDone = true
                            Log.i(VM, "⏪ DELAY seek → ${targetDelay.toInt()}s behind live (buffer=${availableSec.toInt()}s, seekTarget=${"%.1f".format(newest - targetDelay)}s)")
                        }
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
            Log.w(VM, "requireActivePlayer — no player for mode ${_state.value.playbackMode}")
        }
        return p != null
    }

    fun dispatch(action: CriAction) {
        when (action) {
            is CriAction.Play -> {
                _state.value = _state.value.copy(error = null)
                when (_state.value.playbackMode) {
                    PlaybackMode.LIVE_STREAMING -> {
                        if (!requirePlayer()) return
                        Log.i(VM, "play server=${action.serverUrl}")
                        val url = "${action.serverUrl}/hls/playlist.m3u8"
                        val wasPaused = _state.value.playbackState == PlaybackState.PAUSED
                        if (wasPaused && action.serverUrl == currentServerUrl) {
                            Log.i(VM, "play resuming from paused position")
                            player.resume()
                        } else {
                            Log.i(VM, "play new stream")
                            currentServerUrl = action.serverUrl
                            subtitleSource.connect(action.serverUrl)
                            player.play(url)
                            initialDelaySeekDone = false
                        }
                    }
                    PlaybackMode.OFFLINE_SAVED -> {
                        val op = offlinePlayer
                        if (op == null) {
                            Log.w(VM, "play offline — no offline player")
                            return
                        }
                        Log.i(VM, "play offline")
                        op.play("")
                    }
                }
                _state.value = _state.value.copy(isPronouncing = false)
            }
            CriAction.Pause -> {
                val ap = activePlayerOrNull() ?: return
                Log.i(VM, "pause")
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
                Log.i(VM, "resume")
                ap.resume()
                _state.value = _state.value.copy(isPronouncing = false)
            }
            is CriAction.WordTapped -> {
                val ap = activePlayerOrNull() ?: return
                Log.i(VM, "word_tapped text=${action.word.text} pinyin=${action.word.pinyin}")
                ap.pause()
                // Use the segments from state (set by whichever source is active)
                val segments = _state.value.segments
                val engine = SubtitleSyncEngine(segments)
                val segment = engine.findSegmentForWord(action.word, segments)
                val timelineMs = engine.getWordTimelineMs(action.word)

                val currentActive = _state.value.activeWord
                if (currentActive != action.word) {
                    ap.seekTo(timelineMs)
                }

                _state.value = _state.value.copy(
                    wordPopup = WordPopupState(
                        word = action.word,
                        segment = segment ?: return,
                        pinyin = action.word.pinyin,
                        translation = action.word.translation
                    )
                )
                savedWord.value = action.word
            }
            CriAction.DismissPopup -> {
                _state.value = _state.value.copy(wordPopup = null, isPronouncing = false)
            }
            CriAction.PronounceWord -> {
                Log.i(VM, "pronounce_word")
                val word = savedWord.value ?: return
                val allWords = _state.value.segments.flatMap { it.words }
                val wordIdx = allWords.indexOfFirst { w -> w === word }
                if (wordIdx < 0) {
                    pronunciationPlayer.playWord(word)
                } else {
                    val prevTimeTo = if (wordIdx > 0) allWords[wordIdx - 1].end_sec else null
                    val nextTimeFrom = if (wordIdx < allWords.size - 1) allWords[wordIdx + 1].start_sec else null
                    pronunciationPlayer.playWord(word, prevTimeTo, nextTimeFrom)
                }
                _state.value = _state.value.copy(isPronouncing = true)
            }
            CriAction.SaveWord -> {
                Log.i(VM, "save_word")
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
            CriAction.EnableDebug -> {
                _state.value = _state.value.copy(debugEnabled = true)
                prefs.edit().putBoolean("debug_enabled", true).apply()
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
                        Log.w(VM, "Failed to load archive info: ${e.message}")
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
        }
    }

    // ── Offline mode helpers ───────────────────────────────────────────

    private fun switchPlaybackMode(mode: PlaybackMode) {
        if (mode == _state.value.playbackMode) return
        Log.i(VM, "switchPlaybackMode → $mode")

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
                    initialDelaySeekDone = false
                }
                _state.value = _state.value.copy(
                    playbackMode = mode,
                    playbackState = if (::player.isInitialized) player.playbackState.value else PlaybackState.IDLE,
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
                // Load offline data
                offlineSubtitleSource.load()
                val storedSegments = offlineSubtitleSource.segments.value
                if (storedSegments.isNotEmpty()) {
                    offlinePlayer?.release()
                    offlinePlayer = OfflineRadioPlayer(
                        storedSegments,
                        offlineStorageManager,
                        getApplication()
                    )
                    offlinePlayer?.pause()
                    // Collect offline player state so Play/Pause button responds
                    val op = offlinePlayer!!
                    offlineStateJob?.cancel()
                    offlineStateJob = viewModelScope.launch {
                        op.playbackState.collect { ps ->
                            if (_state.value.playbackMode == PlaybackMode.OFFLINE_SAVED) {
                                _state.value = _state.value.copy(playbackState = ps)
                            }
                        }
                    }
                }
                _state.value = _state.value.copy(
                    playbackMode = mode,
                    playbackState = offlinePlayer?.playbackState?.value ?: PlaybackState.IDLE,
                    segments = storedSegments,
                    offlineLocalRangeSec = offlineStorageManager.computeLocalRange(),
                    offlinePositionMs = 0L,  // start of content = 0 relative
                    error = null  // UI shows OfflineSetupScreen when no segments
                )
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
                    _state.value = _state.value.copy(segments = offlineSubtitleSource.segments.value)
                }
            }
        }
    }

    companion object {
        private const val VM = "CRIRadio:vm"
        private const val DELAY_TARGET_SEC = 45     // target buffer behind live edge
        private const val MIN_BUFFER_FOR_DELAY_SEEK = 5  // segments needed before initial seek
    }

    override fun onCleared() {
        super.onCleared()
        pronunciationPlayer.stop()
        subtitleSource.disconnect()
        downloadJob?.cancel()
        offlinePlayer?.release()
        // Live player is owned by PlayerService — do NOT release it here.
        // The service survives Activity destruction and keeps audio alive.
    }
}

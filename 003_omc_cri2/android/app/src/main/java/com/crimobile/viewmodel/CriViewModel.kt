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
    private val pronunciationPlayer by lazy { PronunciationPlayer(player, viewModelScope) }

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
            metadataProtocol = prefs.getString("metadata_protocol", "HTTP") ?: "HTTP",
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
                val isOffline = _state.value.playbackMode == PlaybackMode.OFFLINE_SAVED

                if (isOffline) {
                    // ── Offline mode: lightweight SegmentMeta + lazy SegmentCache ──
                    val segmentsMeta = _state.value.segmentsMeta
                    if (segmentsMeta.isNotEmpty()) {
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

                        _state.value = _state.value.copy(
                            activeSegment = fullSeg,
                            activeSegmentId = activeSegmentMeta?.segment_id,
                            activeWord = activeWord,
                            subtitleDelaySec = delay,
                            lastActiveWord = if (activeWord != null) activeWord else _state.value.lastActiveWord,
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
                            Log.i(VM, "▶seg id=${activeSegmentMeta.segment_id} " +
                                "segTL=[${activeSegmentMeta.timeline_start_sec}-${activeSegmentMeta.timeline_end_sec}] " +
                                "playerSec=${"%.1f".format(playerSec)} text=${fullSeg?.text_zh?.take(50) ?: activeSegmentMeta.text_zh.take(50)}")
                        }

                        if (activeWord != null && activeWord !== lastActiveWord) {
                            lastActiveWord = activeWord
                            val relStart = activeWord.start_sec - (activeSegmentMeta.timeline_start_sec)
                            val relEnd = activeWord.end_sec - (activeSegmentMeta.timeline_start_sec)
                            Log.i(VM, "▷word text=${activeWord.text} " +
                                "wTL=[${activeWord.start_sec}-${activeWord.end_sec}] " +
                                "relTL=[%.3f-%.3f] ".format(relStart, relEnd) +
                                "playerSec=%.3f playerMs=$playerMs".format(playerSec))
                        }

                        val now = System.currentTimeMillis()
                        if (activeSegmentMeta != null && now - lastSyncLog > 2000) {
                            lastSyncLog = now
                            Log.d(VM, "sync playerSec=%.1f segId=${activeSegmentMeta.segment_id} ".format(playerSec) +
                                "segTL=[${activeSegmentMeta.timeline_start_sec}-${activeSegmentMeta.timeline_end_sec}] " +
                                "word=${activeWord?.text} wTL=[${activeWord?.start_sec}-${activeWord?.end_sec}] " +
                                "delay=${delay.toInt()}s")
                        }

                        if (activeSegmentMeta == null && now - lastSyncLog > 2000) {
                            lastSyncLog = now
                            val first = segmentsMeta.firstOrNull()
                            val last = segmentsMeta.lastOrNull()
                            Log.w(VM, "sync MISS: no active segment. playerSec=%.1f".format(playerSec) +
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

                        _state.value = _state.value.copy(
                            activeSegment = activeSegment,
                            activeSegmentId = activeSegmentMeta?.segment_id,
                            activeWord = activeWord,
                            subtitleDelaySec = delay,
                            lastActiveWord = if (activeWord != null) activeWord else _state.value.lastActiveWord,
                            offlinePositionMs = _state.value.offlinePositionMs,
                            offlineDurationMs = _state.value.offlineDurationMs
                        )

                        if (activeSegment != null && activeSegment.segment_id != lastActiveSegId) {
                            lastActiveSegId = activeSegment.segment_id
                            Log.i(VM, "▶seg id=${activeSegment.segment_id} " +
                                "segTL=[${activeSegment.timeline_start_sec}-${activeSegment.timeline_end_sec}] " +
                                "playerSec=${"%.1f".format(playerSec)} text=${activeSegment.text_zh.take(50)}")
                        }

                        if (activeWord != null && activeWord !== lastActiveWord) {
                            lastActiveWord = activeWord
                            val relStart = activeWord.start_sec - (activeSegment.timeline_start_sec)
                            val relEnd = activeWord.end_sec - (activeSegment.timeline_start_sec)
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

                        if (activeSegment == null && now - lastSyncLog > 2000) {
                            lastSyncLog = now
                            val first = segments.firstOrNull()
                            val last = segments.lastOrNull()
                            Log.w(VM, "sync MISS: no active segment. playerSec=%.1f".format(playerSec) +
                                " playerMs=$playerMs segs=${segments.size} " +
                                "loadedRange=[${first?.timeline_start_sec}-${last?.timeline_end_sec}] " +
                                "(player ${if (last != null && playerSec > last.timeline_end_sec) "AHEAD of" else if (first != null && playerSec < first.timeline_start_sec) "BEHIND" else "inside?"} window)")
                        }

                        // ── One-shot delay seek: rewind player behind live edge ──
                        if (!initialDelaySeekDone && playerMs > 0 && segments.size >= MIN_BUFFER_FOR_DELAY_SEEK) {
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
            }
            CriAction.DismissPopup -> {
                _state.value = _state.value.copy(wordPopup = null, isPronouncing = false)
            }
            CriAction.PronounceWord -> {
                Log.i(VM, "pronounce_word")
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
            is CriAction.SetDictFontSize -> {
                _state.value = _state.value.copy(dictFontSizeSp = action.sp)
                prefs.edit().putInt("dict_font_size_sp", action.sp).apply()
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
                Log.i(VM, "SetMetadataProtocol → $newProtocol")

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
        private const val DELAY_TARGET_SEC = 45     // target buffer behind live edge
        private const val MIN_BUFFER_FOR_DELAY_SEEK = 5  // segments needed before initial seek
    }

    override fun onCleared() {
        super.onCleared()
        pronunciationPlayer.stop()
        subtitleSource.disconnect()
        downloadJob?.cancel()
        offlinePlayer?.release()
        segmentCache?.clear()
        segmentCache = null
        // Live player is owned by PlayerService — do NOT release it here.
        // The service survives Activity destruction and keeps audio alive.
    }
}

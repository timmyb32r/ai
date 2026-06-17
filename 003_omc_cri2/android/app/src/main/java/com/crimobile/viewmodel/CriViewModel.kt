package com.crimobile.viewmodel

import android.app.Application
import android.content.Context
import android.util.Log
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import com.crimobile.model.*
import com.crimobile.player.ExoRadioPlayer
import com.crimobile.player.RadioPlayer
import com.crimobile.pronounce.PronunciationPlayer
import com.crimobile.subtitles.SseSubtitleSource
import com.crimobile.subtitles.SubtitleSource
import com.crimobile.sync.SubtitleSyncEngine
import com.crimobile.vocabulary.VocabularyStore
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.combine
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
    val wordPopup: WordPopupState? = null,
    val isPronouncing: Boolean = false,  // true while PronounceWord audio plays
    val connectionStatus: ConnectionStatus = ConnectionStatus.DISCONNECTED,
    val error: String? = null,
    val subtitleDelaySec: Double = 0.0,  // how far behind live are subtitles
    val lastActiveWord: WordEntry? = null  // remembered for recenter during silence gaps
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
}

class CriViewModel(application: Application) : AndroidViewModel(application) {

    private val player: RadioPlayer = ExoRadioPlayer(application)
    private val subtitleSource: SubtitleSource = SseSubtitleSource()
    private val pronunciationPlayer = PronunciationPlayer(player, viewModelScope)
    private val vocabularyStore = VocabularyStore(application)

    private val prefs = application.getSharedPreferences("cri_prefs", Context.MODE_PRIVATE)

    private val _state = MutableStateFlow(
        CriViewState(
            showPinyin = prefs.getBoolean("show_pinyin", false),
            fontSizeSp = prefs.getInt("font_size_sp", 22),
            showWordBoundaries = prefs.getBoolean("show_word_boundaries", false),
        )
    )
    val state: StateFlow<CriViewState> = _state.asStateFlow()

    private val savedWord = MutableStateFlow<WordEntry?>(null)
    private var currentServerUrl: String = ""
    private var lastSyncLog = 0L
    private var lastActiveSegId = -1
    private var lastActiveWord: WordEntry? = null

    init {
        viewModelScope.launch {
            while (isActive) {
                val segments = subtitleSource.segments.value
                if (segments.isNotEmpty()) {
                    val engine = SubtitleSyncEngine(segments)
                    val playerMs = player.currentTimelineMs.value
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
                        lastActiveWord = if (activeWord != null) activeWord else _state.value.lastActiveWord
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
                }
                delay(100)
            }
        }

        // Forward connection status
        viewModelScope.launch {
            subtitleSource.connected.collect { status ->
                _state.value = _state.value.copy(connectionStatus = status)
            }
        }

        // Forward segments
        viewModelScope.launch {
            subtitleSource.segments.collect { segs ->
                _state.value = _state.value.copy(segments = segs)
            }
        }

        // Forward playback state
        viewModelScope.launch {
            player.playbackState.collect { ps ->
                _state.value = _state.value.copy(playbackState = ps)
            }
        }
    }

    fun dispatch(action: CriAction) {
        when (action) {
            is CriAction.Play -> {
                Log.i(VM, "play server=${action.serverUrl}")
                val url = "${action.serverUrl}/hls/playlist.m3u8"
                val wasPaused = _state.value.playbackState == PlaybackState.PAUSED
                if (wasPaused && action.serverUrl == currentServerUrl) {
                    // Resume from seeked position (e.g. after WordTapped/DismissPopup)
                    Log.i(VM, "play resuming from paused position")
                    player.resume()
                } else {
                    Log.i(VM, "play new stream")
                    currentServerUrl = action.serverUrl
                    subtitleSource.connect(action.serverUrl)
                    player.play(url)
                }
                _state.value = _state.value.copy(isPronouncing = false)
            }
            CriAction.Pause -> {
                Log.i(VM, "pause")
                player.pause()
                _state.value = _state.value.copy(isPronouncing = false)
            }
            CriAction.Resume -> {
                Log.i(VM, "resume")
                player.resume()
                _state.value = _state.value.copy(isPronouncing = false)
            }
            is CriAction.WordTapped -> {
                Log.i(VM, "word_tapped text=${action.word.text} pinyin=${action.word.pinyin}")
                player.pause()
                val segments = subtitleSource.segments.value
                val engine = SubtitleSyncEngine(segments)
                val segment = engine.findSegmentForWord(action.word, segments)
                val timelineMs = engine.getWordTimelineMs(action.word)

                val currentActive = _state.value.activeWord
                if (currentActive != action.word) {
                    player.seekTo(timelineMs)
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
                pronunciationPlayer.playWord(word)
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
        }
    }

    companion object { private const val VM = "CRIRadio:vm" }

    override fun onCleared() {
        super.onCleared()
        pronunciationPlayer.stop()
        subtitleSource.disconnect()
        player.release()
    }
}

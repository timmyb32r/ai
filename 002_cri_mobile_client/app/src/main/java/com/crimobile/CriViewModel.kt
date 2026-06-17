package com.crimobile

import android.app.Application
import android.content.ContentValues
import android.content.pm.PackageManager
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import android.util.Log
import androidx.core.content.ContextCompat
import androidx.lifecycle.AndroidViewModel
import com.crimobile.api.HighlightTracker
import com.crimobile.api.PlaybackStateMachine
import com.crimobile.model.PlaybackState
import com.crimobile.model.SubtitleEvent
import com.crimobile.model.SyncedSegment
import com.crimobile.model.WordBoundary
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import okhttp3.OkHttpClient
import okhttp3.Request
import java.util.concurrent.TimeUnit

data class WordPopupState(
    val word: String,
    val pinyin: String,
    val english: String,
    val wordBoundary: WordBoundary,
    val subtitle: SubtitleEvent,
)

class CriViewModel(
    application: Application,
    val serverConfig: ServerConfig,
    private val stateMachine: PlaybackStateMachine,
) : AndroidViewModel(application) {

    // ── Observable state (delegated to state machine) ──────────────────

    val playbackState: StateFlow<PlaybackState> = stateMachine.state
    val recentSegments: StateFlow<List<SyncedSegment>> = stateMachine.segments
    val highlightRange: StateFlow<IntRange?> = stateMachine.highlightRange
    val debugLog: StateFlow<List<String>> = stateMachine.debugLog
    val audioFlowing: StateFlow<Boolean> = stateMachine.audioFlowing

    // ── Settings ──────────────────────────────────────────────────────

    private val _showPinyin = MutableStateFlow(serverConfig.showPinyin)
    val showPinyin: StateFlow<Boolean> = _showPinyin

    private val _showTranslation = MutableStateFlow(serverConfig.showTranslation)
    val showTranslation: StateFlow<Boolean> = _showTranslation

    fun setShowPinyin(enabled: Boolean) {
        serverConfig.showPinyin = enabled
        _showPinyin.value = enabled
    }

    fun setShowTranslation(enabled: Boolean) {
        serverConfig.showTranslation = enabled
        _showTranslation.value = enabled
    }

    // ── Server info ───────────────────────────────────────────────────

    val serverHost: String get() = serverConfig.host
    val serverPort: Int get() = serverConfig.port

    private val _delaySeconds = MutableStateFlow<Double?>(null)
    val delaySeconds: StateFlow<Double?> = _delaySeconds

    // ── Word popup ────────────────────────────────────────────────────

    private val _wordPopup = MutableStateFlow<WordPopupState?>(null)
    val wordPopup: StateFlow<WordPopupState?> = _wordPopup

    private var isWordPause = false

    private val _feedbackMessage = MutableStateFlow<String?>(null)
    val feedbackMessage: StateFlow<String?> = _feedbackMessage

    // ── Init ──────────────────────────────────────────────────────────

    init {
        startStatusPolling()
    }

    override fun onCleared() {
        super.onCleared()
        stateMachine.stop()
    }

    fun release() = stateMachine.stop()

    // ── Play / Pause ──────────────────────────────────────────────────

    fun togglePlayPause() {
        if (isWordPause) {
            continueFromWord()
            return
        }
        stateMachine.togglePlayPause()
    }

    fun seekBackward() = stateMachine.seekRelative(-10.0)
    fun seekForward() = stateMachine.seekRelative(10.0)
    fun clearDebugLog() = stateMachine.clearDebugLog()

    fun updateServerConfig(host: String, port: Int) {
        serverConfig.save(host, port)
    }

    // ── Word popup ────────────────────────────────────────────────────

    fun onWordClick(wordBoundary: WordBoundary) {
        val seg = recentSegments.value.firstOrNull { seg ->
            seg.subtitle.words?.contains(wordBoundary) == true
        } ?: return
        val subtitle = seg.subtitle

        isWordPause = true
        stateMachine.onWordClick(wordBoundary, subtitle)

        _wordPopup.value = WordPopupState(
            word = wordBoundary.substring(subtitle.textZh),
            pinyin = wordBoundary.pinyin,
            english = wordBoundary.en,
            wordBoundary = wordBoundary,
            subtitle = subtitle,
        )
    }

    fun dismissPopup() {
        _wordPopup.value = null
        if (isWordPause) continueFromWord()
    }

    private fun continueFromWord() {
        stateMachine.resume()
        isWordPause = false
        _wordPopup.value = null
    }

    fun pronounceWord() {
        val popup = _wordPopup.value ?: return
        stateMachine.pronounceWord(popup.wordBoundary, popup.subtitle)
    }

    fun saveWord() {
        val popup = _wordPopup.value ?: return
        val line = "${popup.word}\t${popup.english}\n"
        val context = getApplication<Application>()
        var success = false

        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
                val contentValues = ContentValues().apply {
                    put(MediaStore.Downloads.DISPLAY_NAME, "cri-vocabulary.txt")
                    put(MediaStore.Downloads.MIME_TYPE, "text/plain")
                    put(MediaStore.Downloads.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS)
                }
                val uri = context.contentResolver.insert(
                    MediaStore.Downloads.EXTERNAL_CONTENT_URI, contentValues
                )
                if (uri != null) {
                    context.contentResolver.openOutputStream(uri)?.use { os ->
                        os.write(line.toByteArray())
                        success = true
                    }
                }
            } else {
                @Suppress("DEPRECATION")
                val permission = ContextCompat.checkSelfPermission(
                    context, android.Manifest.permission.WRITE_EXTERNAL_STORAGE
                )
                if (permission == PackageManager.PERMISSION_GRANTED) {
                    @Suppress("DEPRECATION")
                    val downloadsDir = Environment.getExternalStoragePublicDirectory(
                        Environment.DIRECTORY_DOWNLOADS
                    )
                    val file = java.io.File(downloadsDir, "cri-vocabulary.txt")
                    file.appendText(line)
                    success = true
                } else {
                    _feedbackMessage.value = "Permission denied — enable storage access"
                }
            }
        } catch (e: Exception) {
            Log.e(TAG, "Failed to save word", e)
            _feedbackMessage.value = "Failed to save: ${e.message}"
        }

        if (success) {
            _feedbackMessage.value = "Saved: ${popup.word}"
            _wordPopup.value = null
        }
    }

    // ── Status polling ────────────────────────────────────────────────

    private fun startStatusPolling() {
        val okHttp = OkHttpClient.Builder()
            .connectTimeout(10, TimeUnit.SECONDS)
            .build()
        Thread {
            while (true) {
                try {
                    val request = Request.Builder()
                        .url("${serverConfig.baseUrl}/v1/status")
                        .build()
                    val response = okHttp.newCall(request).execute()
                    if (response.isSuccessful) {
                        val body = response.body?.string()
                        if (body != null) {
                            val json = org.json.JSONObject(body)
                            _delaySeconds.value = json.optDouble("delaySeconds", -1.0)
                        }
                    }
                } catch (_: Exception) {}
                try { Thread.sleep(5000) } catch (_: InterruptedException) { break }
            }
        }.also { it.isDaemon = true; it.start() }
    }

    companion object {
        private const val TAG = "CriViewModel"
    }
}

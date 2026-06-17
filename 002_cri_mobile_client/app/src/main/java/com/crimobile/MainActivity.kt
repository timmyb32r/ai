package com.crimobile

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.ui.Modifier
import androidx.lifecycle.ViewModel
import androidx.lifecycle.ViewModelProvider
import androidx.media3.common.AudioAttributes
import androidx.media3.common.C
import androidx.media3.exoplayer.ExoPlayer
import com.crimobile.api.PlaybackStateMachine
import com.crimobile.infrastructure.PCMRingBuffer
import com.crimobile.service.HighlightTrackerImpl
import com.crimobile.service.PlaybackStateMachineImpl
import com.crimobile.service.SseSubtitleSource
import com.crimobile.ui.CriApp
import okhttp3.OkHttpClient
import java.util.concurrent.TimeUnit

class MainActivity : ComponentActivity() {

    private lateinit var viewModel: CriViewModel
    private var exoPlayer: ExoPlayer? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        val app = application
        val serverConfig = ServerConfig(app)

        // ── Build-time DI wiring ──────────────────────────────────────
        val okHttp = OkHttpClient.Builder()
            .connectTimeout(10, TimeUnit.SECONDS)
            .readTimeout(0, TimeUnit.MILLISECONDS)
            .build()
        val pcmBuffer = PCMRingBuffer()
        val subtitleSource = SseSubtitleSource(pcmBuffer, okHttp)
        val highlightTracker = HighlightTrackerImpl()

        exoPlayer = ExoPlayer.Builder(this)
            .setAudioAttributes(
                AudioAttributes.Builder()
                    .setUsage(C.USAGE_MEDIA)
                    .setContentType(C.AUDIO_CONTENT_TYPE_SPEECH)
                    .build(),
                /* handleAudioFocus = */ true,
            )
            .build()

        val stateMachine: PlaybackStateMachine = PlaybackStateMachineImpl(
            app, serverConfig, subtitleSource, highlightTracker, pcmBuffer,
            exoPlayer!!,
        )

        val factory = object : ViewModelProvider.Factory {
            @Suppress("UNCHECKED_CAST")
            override fun <T : ViewModel> create(modelClass: Class<T>): T {
                return CriViewModel(app, serverConfig, stateMachine) as T
            }
        }
        viewModel = ViewModelProvider(this, factory)[CriViewModel::class.java]

        setContent {
            MaterialTheme {
                Surface(
                    modifier = Modifier.fillMaxSize(),
                    color = MaterialTheme.colorScheme.background,
                ) {
                    CriApp(viewModel = viewModel)
                }
            }
        }
    }

    override fun onDestroy() {
        super.onDestroy()
        if (isFinishing) {
            viewModel.release()
            exoPlayer?.release()
            exoPlayer = null
        }
    }
}

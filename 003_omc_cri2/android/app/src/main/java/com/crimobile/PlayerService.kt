package com.crimobile

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.media3.common.AudioAttributes
import androidx.media3.common.C
import androidx.media3.common.util.UnstableApi
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.source.DefaultMediaSourceFactory
import androidx.media3.session.MediaSession
import androidx.media3.session.MediaSessionService
import com.crimobile.model.PlaybackState
import com.crimobile.player.ExoRadioPlayer
import com.crimobile.player.RadioPlayerHolder
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.launch
import com.crimobile.debug.DebugLogger

/**
 * Foreground service that owns the ExoPlayer and the Media3 session.
 *
 * The **single** ExoPlayer is shared by both [ExoRadioPlayer] (audio)
 * and [MediaSession] (metadata / token for the notification widget).
 * This unification is what makes the media widget appear — the platform
 * receives the **same** session token via [onGetSession] and the
 * notification's [NotificationCompat.MediaStyle].
 *
 * Audio continues when the screen turns off because:
 * 1. The service is in the foreground (on-going notification → process stays alive).
 * 2. [C.WAKE_MODE_NETWORK] keeps the CPU + Wi-Fi awake during playback.
 */
@UnstableApi
class PlayerService : MediaSessionService() {

    private lateinit var exoPlayer: ExoPlayer
    private lateinit var player: ExoRadioPlayer
    private lateinit var mediaSession: MediaSession
    private var stateCollectJob: Job? = null
    private val scope = CoroutineScope(Dispatchers.Main)

    private var lastIsPlaying: Boolean = false

    override fun onCreate() {
        super.onCreate()

        DebugLogger.i(TAG, "onCreate — creating ExoPlayer and MediaSession")

        // 1. Single ExoPlayer — used for both audio playback and MediaSession.
        //    Audio focus + becoming-noisy handling so the stream pauses (not plays
        //    over) incoming calls, other media apps, and headphone disconnects.
        exoPlayer = ExoPlayer.Builder(this)
            .setMediaSourceFactory(
                DefaultMediaSourceFactory(this).setLiveTargetOffsetMs(3000)
            )
            .setAudioAttributes(
                AudioAttributes.Builder()
                    .setUsage(C.USAGE_MEDIA)
                    .setContentType(C.AUDIO_CONTENT_TYPE_MUSIC)
                    .build(),
                /* handleAudioFocus = */ true
            )
            .setHandleAudioBecomingNoisy(true)
            .build()
            .apply {
                setWakeMode(C.WAKE_MODE_NETWORK) // keep CPU + Wi-Fi awake
            }

        // 2. Radio player wraps the same ExoPlayer.
        player = ExoRadioPlayer(exoPlayer)
        RadioPlayerHolder.setPlayer(player)

        // 3. Media3 session — the single session used by both the platform
        //    (onGetSession) and the notification widget (getSessionCompatToken).
        mediaSession = MediaSession.Builder(this, exoPlayer).build()

        // 4. Notification channel (Android 8+)
        createNotificationChannel()

        // 5. Start foreground (Android 14+ requires explicit service type)
        val notification = buildNotification(isPlaying = false)
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(NOTIFICATION_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK)
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }

        // 6. Keep notification text / action button in sync with player state
        stateCollectJob = scope.launch {
            player.playbackState.collect { state ->
                val isPlaying = state == PlaybackState.PLAYING
                if (isPlaying != lastIsPlaying) {
                    lastIsPlaying = isPlaying
                    val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
                    nm.notify(NOTIFICATION_ID, buildNotification(isPlaying))
                }
            }
        }
    }

    override fun onGetSession(controllerInfo: MediaSession.ControllerInfo): MediaSession? = mediaSession

    override fun onTaskRemoved(rootIntent: Intent?) {
        // Keep the service alive when the user swipes the app away so the next
        // launch reuses the already-running player (instant Play). Killing it on
        // swipe previously forced a 10s PlayerService restart timeout on reopen
        // because startForegroundService from Application.onCreate was deferred.
        // FGS-policy trade-off: accepted in favour of cold-start speed.
        DebugLogger.i(TAG, "onTaskRemoved — keeping service alive (foreground notification)")
        super.onTaskRemoved(rootIntent)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_PLAY -> {
                DebugLogger.i(TAG, "onStartCommand ACTION_PLAY")
                player.resume()
            }
            ACTION_PAUSE -> {
                DebugLogger.i(TAG, "onStartCommand ACTION_PAUSE")
                player.pause()
            }
            else -> {
                // MediaSessionService handles ACTION_MEDIA_BUTTON (headset, Bluetooth,
                // Android Auto) in its own onStartCommand — delegate so media keys
                // reach the session.
                return super.onStartCommand(intent, flags, startId)
            }
        }
        return START_STICKY
    }

    override fun onDestroy() {
        DebugLogger.i(TAG, "onDestroy — releasing player")
        stateCollectJob?.cancel()
        scope.cancel()
        // Media3 requires the MediaSession to be released BEFORE the ExoPlayer it
        // references; otherwise the session may touch a released player during cleanup.
        mediaSession.release()
        player.release()
        RadioPlayerHolder.clearPlayer()
        stopForeground(STOP_FOREGROUND_REMOVE)
        super.onDestroy()
    }

    // ── Notification ──────────────────────────────────────────────────

    private fun createNotificationChannel() {
        val channel = NotificationChannel(
            CHANNEL_ID,
            "CRI Radio",
            NotificationManager.IMPORTANCE_LOW  // media — no sound, shows in shade
        ).apply {
            description = "Ongoing playback notification"
            setShowBadge(false)
        }
        val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
        nm.createNotificationChannel(channel)
    }

    private fun buildNotification(isPlaying: Boolean): Notification {
        val contentIntent = PendingIntent.getActivity(
            this,
            0,
            packageManager.getLaunchIntentForPackage(packageName),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )

        val toggleAction = if (isPlaying) ACTION_PAUSE else ACTION_PLAY
        val toggleIcon = if (isPlaying) android.R.drawable.ic_media_pause
        else android.R.drawable.ic_media_play
        val toggleLabel = if (isPlaying) "Pause" else "Play"

        val toggleIntent = Intent(this, PlayerService::class.java).apply {
            action = toggleAction
        }
        val togglePending = PendingIntent.getService(
            this, 1, toggleIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )

        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setContentTitle("CRI Radio")
            .setContentText(if (isPlaying) "Live broadcast playing…" else "Playback paused")
            .setSmallIcon(android.R.drawable.ic_media_play)
            .setContentIntent(contentIntent)
            .setOngoing(true)
            // ── Media widget: same session token that onGetSession() returns ──
            .setStyle(
                androidx.media.app.NotificationCompat.MediaStyle()
                    .setMediaSession(mediaSession.getSessionCompatToken())
                    .setShowActionsInCompactView(0)
            )
            .addAction(toggleIcon, toggleLabel, togglePending)
            .build()
    }

    companion object {
        private const val TAG = "CRIRadio:service"
        private const val CHANNEL_ID = "cri_radio"
        private const val NOTIFICATION_ID = 101
        private const val ACTION_PLAY = "com.crimobile.action.PLAY"
        private const val ACTION_PAUSE = "com.crimobile.action.PAUSE"
    }
}

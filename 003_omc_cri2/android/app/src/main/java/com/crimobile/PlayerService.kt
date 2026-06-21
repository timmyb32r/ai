package com.crimobile

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.support.v4.media.MediaMetadataCompat
import android.support.v4.media.session.MediaSessionCompat
import android.support.v4.media.session.PlaybackStateCompat
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.media3.common.util.UnstableApi
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.session.MediaSession
import androidx.media3.session.MediaSessionService
import com.crimobile.model.PlaybackState
import com.crimobile.player.ExoRadioPlayer
import com.crimobile.player.RadioPlayerHolder
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.launch

/**
 * Foreground service that owns the ExoRadioPlayer.
 * Audio continues when the screen turns off because the service
 * is in the foreground (on-going notification), which prevents
 * the system from killing the process.
 *
 * The media widget appears in the notification shade via
 * [NotificationCompat.MediaStyle] + [MediaSessionCompat].
 */
@UnstableApi
class PlayerService : MediaSessionService() {

    private lateinit var player: ExoRadioPlayer
    private lateinit var mediaSession: MediaSession
    private lateinit var mediaSessionCompat: MediaSessionCompat
    private var stateCollectJob: Job? = null
    private val scope = CoroutineScope(Dispatchers.Main)

    private var lastIsPlaying: Boolean = false

    override fun onCreate() {
        super.onCreate()

        Log.i(TAG, "onCreate — creating player and MediaSession")

        // 1. Create the radio player (owns the real ExoPlayer)
        player = ExoRadioPlayer(applicationContext)
        RadioPlayerHolder.setPlayer(player)

        // 2. Media3 MediaSession — required by the framework for onGetSession.
        val sessionPlayer = ExoPlayer.Builder(this).build()
        sessionPlayer.playWhenReady = false

        mediaSession = MediaSession.Builder(this, sessionPlayer).build()

        // 3. MediaSessionCompat — provides the token for NotificationCompat.setMediaSession()
        //    so the system recognises this as a media notification and shows the widget.
        mediaSessionCompat = MediaSessionCompat(this, "CRIRadio").apply {
            setFlags(
                MediaSessionCompat.FLAG_HANDLES_MEDIA_BUTTONS
                    or MediaSessionCompat.FLAG_HANDLES_TRANSPORT_CONTROLS
            )
            setCallback(object : MediaSessionCompat.Callback() {
                override fun onPlay() = player.resume()
                override fun onPause() = player.pause()
            })
            setMetadata(
                MediaMetadataCompat.Builder()
                    .putString(MediaMetadataCompat.METADATA_KEY_TITLE, "CRI Radio")
                    .putString(MediaMetadataCompat.METADATA_KEY_ARTIST, "Live Broadcast")
                    .build()
            )
            // Set initial playback state so the media widget shows immediately
            setPlaybackState(
                PlaybackStateCompat.Builder()
                    .setState(PlaybackStateCompat.STATE_STOPPED, PlaybackStateCompat.PLAYBACK_POSITION_UNKNOWN, 1.0f)
                    .setActions(
                        PlaybackStateCompat.ACTION_PLAY
                            or PlaybackStateCompat.ACTION_PAUSE
                            or PlaybackStateCompat.ACTION_STOP
                    )
                    .build()
            )
            isActive = true
        }

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
                    updateMediaSessionState(isPlaying)
                    val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
                    nm.notify(NOTIFICATION_ID, buildNotification(isPlaying))
                }
            }
        }
    }

    override fun onGetSession(controllerInfo: MediaSession.ControllerInfo): MediaSession? = mediaSession

    override fun onTaskRemoved(rootIntent: Intent?) {
        Log.i(TAG, "onTaskRemoved — stopping self")
        stopSelf()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_PLAY -> player.resume()
            ACTION_PAUSE -> player.pause()
        }
        return START_NOT_STICKY
    }

    override fun onDestroy() {
        Log.i(TAG, "onDestroy — releasing player")
        stateCollectJob?.cancel()
        player.release()
        RadioPlayerHolder.clearPlayer()
        mediaSessionCompat.release()
        mediaSession.release()
        stopForeground(STOP_FOREGROUND_REMOVE)
        super.onDestroy()
    }

    // ── Notification ──────────────────────────────────────────────

    private fun createNotificationChannel() {
        val channel = NotificationChannel(
            CHANNEL_ID,
            "CRI Radio",
            NotificationManager.IMPORTANCE_LOW  // media playback — no sound, shows in shade
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

        // Toggle play/pause via a service intent
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
            // ── Media widget: MediaStyle carries the session token ──
            .setStyle(
                androidx.media.app.NotificationCompat.MediaStyle()
                    .setMediaSession(mediaSessionCompat.sessionToken)
                    .setShowActionsInCompactView(0)
            )
            // ── Action button ──
            .addAction(toggleIcon, toggleLabel, togglePending)
            .build()
    }

    private fun updateMediaSessionState(isPlaying: Boolean) {
        val state = if (isPlaying) PlaybackStateCompat.STATE_PLAYING
        else PlaybackStateCompat.STATE_PAUSED
        mediaSessionCompat.setPlaybackState(
            PlaybackStateCompat.Builder()
                .setState(state, PlaybackStateCompat.PLAYBACK_POSITION_UNKNOWN, 1.0f)
                .setActions(
                    PlaybackStateCompat.ACTION_PLAY
                        or PlaybackStateCompat.ACTION_PAUSE
                        or PlaybackStateCompat.ACTION_STOP
                )
                .build()
        )
    }

    companion object {
        private const val TAG = "CRIRadio:service"
        private const val CHANNEL_ID = "cri_radio"
        private const val NOTIFICATION_ID = 101
        private const val ACTION_PLAY = "com.crimobile.action.PLAY"
        private const val ACTION_PAUSE = "com.crimobile.action.PAUSE"
    }
}

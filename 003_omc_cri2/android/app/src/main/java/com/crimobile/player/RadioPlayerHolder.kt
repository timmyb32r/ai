package com.crimobile.player

import android.util.Log
import kotlinx.coroutines.TimeoutCancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.withTimeout

/**
 * Singleton bridge — PlayerService writes the player here, CriViewModel reads it.
 * Zero IPC: the same StateFlow<Long> (currentTimelineMs) is used directly,
 * preserving the 100ms sync precision.
 */
object RadioPlayerHolder {
    private val _player = MutableStateFlow<RadioPlayer?>(null)

    /** Synchronous read. May be null before PlayerService starts. */
    val current: RadioPlayer? get() = _player.value

    fun setPlayer(player: RadioPlayer) {
        _player.value = player
    }

    /**
     * Suspends until the player is available, with a [timeoutMs] fallback.
     * If PlayerService hasn't set the player within the timeout, returns null
     * so the ViewModel can show an error instead of hanging permanently.
     */
    suspend fun awaitPlayer(timeoutMs: Long = 10_000L): RadioPlayer? {
        return try {
            withTimeout(timeoutMs) {
                _player.filterNotNull().first()
            }
        } catch (e: TimeoutCancellationException) {
            Log.w(TAG, "Timed out waiting for PlayerService after ${timeoutMs}ms")
            null
        }
    }

    fun clearPlayer() {
        _player.value = null
    }

    private const val TAG = "CRIRadio:holder"
}

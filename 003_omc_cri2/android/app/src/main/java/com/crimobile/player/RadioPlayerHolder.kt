package com.crimobile.player

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.first

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

    /** Suspends until the player is available. Used by ViewModel init. */
    suspend fun awaitPlayer(): RadioPlayer {
        return _player.filterNotNull().first()
    }

    fun clearPlayer() {
        _player.value = null
    }
}

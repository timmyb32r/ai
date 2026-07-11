package com.crimobile.pronounce

import com.crimobile.model.WordEntry
import com.crimobile.player.RadioPlayer
import com.crimobile.model.PlaybackState
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test

/**
 * Verifies that [PronunciationPlayer] uses whichever player the
 * [playerProvider] lambda returns — not a fixed player hardcoded at
 * construction time. This is critical for offline mode correctness.
 */
class PronunciationPlayerTest {

    /** Minimal fake [RadioPlayer] that records calls and holds mutable state. */
    private class FakeRadioPlayer(
        initialTimelineMs: Long = 100_000L
    ) : RadioPlayer {
        private val _currentTimelineMs = MutableStateFlow(initialTimelineMs)
        override val currentTimelineMs: StateFlow<Long> = _currentTimelineMs

        override val playbackState: StateFlow<PlaybackState> =
            MutableStateFlow(PlaybackState.PAUSED)
        override val behindLiveWindow: StateFlow<Boolean> =
            MutableStateFlow(false)
        override val lastErrorMessage: StateFlow<String?> =
            MutableStateFlow(null)

        var pauseCount = 0
        var resumeCount = 0
        var lastSeekMs: Long? = null
        var released = false

        fun setTimelineMs(ms: Long) { _currentTimelineMs.value = ms }

        override fun play(hlsUrl: String) {}
        override fun pause() { pauseCount++ }
        override fun resume() { resumeCount++ }
        override fun seekTo(timelineMs: Long) { lastSeekMs = timelineMs }
        override fun seekToLiveEdge() {}
        override fun release() { released = true }
    }

    private val testWord = WordEntry(
        text = "试点", char_start = 0, char_end = 2,
        start_sec = 100.0, end_sec = 102.0,
        pinyin = "shìdiǎn", translation = "pilot"
    )

    @Test
    fun `playerProvider is called — uses the returned player`() = runTest {
        val fake = FakeRadioPlayer(initialTimelineMs = 50_000L)
        var providerCalls = 0
        val pp = PronunciationPlayer(
            playerProvider = { providerCalls++; fake },
            scope = this
        )

        pp.playWord(testWord)
        testScheduler.runCurrent()

        assertEquals("provider called", 1, providerCalls)
        assertTrue("player was paused at least once", fake.pauseCount > 0)
        assertEquals("player was resumed", 1, fake.resumeCount)
        assertEquals("seeked to word start epoch ms", 100_000L, fake.lastSeekMs)

        pp.stop()
    }

    @Test
    fun `switching provider changes which player is used`() = runTest {
        val livePlayer = FakeRadioPlayer(initialTimelineMs = 60_000L)
        val offlinePlayer = FakeRadioPlayer(initialTimelineMs = 70_000L)
        var useOffline = false
        val pp = PronunciationPlayer(
            playerProvider = { if (useOffline) offlinePlayer else livePlayer },
            scope = this
        )

        // First call — live player
        pp.playWord(testWord)
        testScheduler.runCurrent()
        // Verify live player was seeked to the word position BEFORE stop overwrites it.
        assertEquals("live player seeked to word start", 100_000L, livePlayer.lastSeekMs)
        assertTrue("live player was paused", livePlayer.pauseCount > 0)
        pp.stop()
        assertEquals("offline player untouched", 0, offlinePlayer.pauseCount)

        // Switch mode
        useOffline = true

        // Second call — offline player
        pp.playWord(testWord.copy(start_sec = 105.0, end_sec = 107.0))
        testScheduler.runCurrent()
        assertEquals("offline player seeked to word start", 105_000L, offlinePlayer.lastSeekMs)
        assertTrue("offline player was paused", offlinePlayer.pauseCount > 0)
        pp.stop()
    }

    @Test
    fun `null provider — no crash, no player calls`() = runTest {
        val fake = FakeRadioPlayer()
        val pp = PronunciationPlayer(
            playerProvider = { null }, // simulate no active player
            scope = this
        )

        pp.playWord(testWord)
        testScheduler.runCurrent()

        assertEquals("no pause when no player", 0, fake.pauseCount)
        assertEquals("no resume when no player", 0, fake.resumeCount)
        assertNull("no seek when no player", fake.lastSeekMs)
    }

    @Test
    fun `stop seeks back to saved position`() = runTest {
        val fake = FakeRadioPlayer(initialTimelineMs = 200_000L)
        val pp = PronunciationPlayer(
            playerProvider = { fake },
            scope = this
        )

        pp.playWord(testWord)
        testScheduler.runCurrent()

        // stop() seeks back to the saved original position.
        pp.stop()
        assertEquals("stop seeks to saved position", 200_000L, fake.lastSeekMs)
    }
}

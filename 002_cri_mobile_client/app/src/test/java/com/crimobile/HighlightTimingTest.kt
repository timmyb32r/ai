package com.crimobile

import com.crimobile.api.HighlightTracker
import org.junit.Assert.assertEquals
import org.junit.Test

class HighlightTimingTest {

    private fun calc(
        relAudioSec: Double,
        relStart: Double,
        relEnd: Double,
        textLength: Int,
    ): Int = HighlightTracker.calculateHighlightIndex(relAudioSec, relStart, relEnd, textLength)

    // ── Basic interpolation ─────────────────────────────────────────────────

    @Test
    fun `at window start — character 0`() {
        assertEquals(0, calc(0.0, 0.0, 8.0, 40))
    }

    @Test
    fun `25 percent in — character 10 of 40`() {
        assertEquals(10, calc(2.0, 0.0, 8.0, 40))
    }

    @Test
    fun `50 percent in — character 20 of 40`() {
        assertEquals(20, calc(4.0, 0.0, 8.0, 40))
    }

    @Test
    fun `75 percent in — character 30 of 40`() {
        assertEquals(30, calc(6.0, 0.0, 8.0, 40))
    }

    @Test
    fun `at window end — last character`() {
        assertEquals(39, calc(8.0, 0.0, 8.0, 40))
    }

    @Test
    fun `single character text`() {
        assertEquals(0, calc(4.0, 0.0, 8.0, 1))
    }

    // ── Boundary conditions ─────────────────────────────────────────────────

    @Test
    fun `before window — no match`() {
        assertEquals(-1, calc(-1.0, 0.0, 8.0, 40))
    }

    @Test
    fun `after window — no match`() {
        assertEquals(-1, calc(9.0, 0.0, 8.0, 40))
    }

    @Test
    fun `empty text — no match`() {
        assertEquals(-1, calc(4.0, 0.0, 8.0, 0))
    }

    @Test
    fun `zero duration — no match`() {
        assertEquals(-1, calc(4.0, 5.0, 5.0, 40))
    }

    @Test
    fun `negative duration — no match`() {
        assertEquals(-1, calc(4.0, 8.0, 0.0, 40))
    }

    // ── Relative-timing scenarios ───────────────────────────────────────────

    // The server has a 180 s delay.  Both audio and subtitles are served from
    // the same broadcast head.  When the first subtitle arrives we record its
    // broadcast-timeline `start` AND the current audio position.  Every later
    // subtitle is matched in *relative* seconds so the alignment is always
    // correct, regardless of when audio actually begins.

    @Test
    fun `first subtitle arrives at audio pos 0 — window starts at rel 0`() {
        // anchor: sub.start=824.0, audioPos=0.0
        // Later subtitles are shifted by anchorSubtitleStart
        val relStart = 824.0 - 824.0 // = 0.0
        val relEnd = 832.0 - 824.0   // = 8.0
        assertEquals(0, calc(0.0, relStart, relEnd, 40))
        assertEquals(10, calc(2.0, relStart, relEnd, 40))
        assertEquals(20, calc(4.0, relStart, relEnd, 40))
    }

    @Test
    fun `first subtitle arrives at audio pos 3s — window starts at rel -3`() {
        // Audio has been playing 3 s before the first subtitle event arrives.
        // anchor: sub.start=824.0, audioPos=3.0
        // Relative window: [824-824, 832-824] = [0, 8]
        // relAudioSec = audioPos - anchorAudioPos = audioPos - 3.0
        // When media position is 3 s, relAudioSec = 0 → at relative start
        val relStart = 0.0; val relEnd = 8.0
        assertEquals(0, calc(0.0, relStart, relEnd, 40))   // relAudio at window start
        assertEquals(20, calc(4.0, relStart, relEnd, 40))   // 4 s in = 50 %
    }

    @Test
    fun `second subtitle arrives — correct relative window`() {
        // First sub: start=824, arrives at audioPos=0 → anchor is 824/0
        // Second sub: start=832 (rel: 832-824=8), end=840 (rel: 840-824=16)
        val relStart = 832.0 - 824.0 // = 8.0
        val relEnd = 840.0 - 824.0   // = 16.0
        assertEquals(-1, calc(7.9, relStart, relEnd, 40))   // just before
        assertEquals(0, calc(8.0, relStart, relEnd, 40))    // at start
        assertEquals(20, calc(12.0, relStart, relEnd, 40))  // halfway
        assertEquals(39, calc(16.0, relStart, relEnd, 40))  // at end
        assertEquals(-1, calc(16.1, relStart, relEnd, 40))  // just after
    }

    // ── The bug we're fixing ────────────────────────────────────────────────

    @Test
    fun `highlight never runs ahead — full sweep`() {
        // Simulate 5 subtitle windows over 40 s of audio
        val textLen = 40
        for (window in 0..4) {
            val relStart = window * 8.0
            val relEnd = relStart + 8.0
            for (offset in 0..16 step 2) {
                val relAudio = relStart - 1.0 + offset / 2.0
                val idx = calc(relAudio, relStart, relEnd, textLen)
                if (relAudio < relStart || relAudio > relEnd) {
                    assertEquals("window=$window offset=$offset", -1, idx)
                } else {
                    val expectedFrac = (relAudio - relStart) / 8.0
                    val expected = (expectedFrac * textLen).toInt().coerceIn(0, textLen - 1)
                    assertEquals("window=$window offset=$offset", expected, idx)
                }
            }
        }
    }

    @Test
    fun `highlight at 0 percent when audio reaches window start`() {
        // This was the core bug: with wrong timing, highlight showed 50% when
        // audio just reached the subtitle's start position.
        assertEquals(0, calc(0.0, 0.0, 8.0, 40))
        assertEquals(0, calc(4.0, 4.0, 12.0, 40))
        assertEquals(0, calc(12.0, 12.0, 20.0, 40))
    }

    @Test
    fun `grazing the window boundaries`() {
        assertEquals(-1, calc(-0.001, 0.0, 8.0, 40))
        assertEquals(0, calc(0.0, 0.0, 8.0, 40))
        assertEquals(39, calc(8.0, 0.0, 8.0, 40))
        assertEquals(-1, calc(8.001, 0.0, 8.0, 40))
    }
}

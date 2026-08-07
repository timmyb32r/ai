package com.crimobile.subtitles

import org.junit.Assert.*
import org.junit.Test

/**
 * Regression tests for the subtitle poll / SSE reconnect backoff overflow.
 *
 * Previously an unbounded `1L shl n` overflowed Long at n >= 63 and produced a
 * negative delay (→ delay() no-op → tight retry loop). [Backoff] clamps it.
 */
class BackoffTest {

    @Test
    fun `no overflow at pathological failure counts`() {
        for (failures in listOf(63, 100, 1000, Int.MAX_VALUE)) {
            val ms = Backoff.computeMs(failures, threshold = 5, baseMs = 1500, maxMs = 60_000)
            assertTrue("failures=$failures → delay must be positive, was $ms", ms > 0)
            assertTrue("failures=$failures → delay must be <= cap, was $ms", ms <= 60_000)
        }
    }

    @Test
    fun `backoff grows exponentially then clamps at max`() {
        assertEquals(1500L, Backoff.computeMs(5, 5, 1500, 60_000))   // shift 0
        assertEquals(3000L, Backoff.computeMs(6, 5, 1500, 60_000))   // shift 1
        assertEquals(6000L, Backoff.computeMs(7, 5, 1500, 60_000))   // shift 2
        assertEquals(12_000L, Backoff.computeMs(8, 5, 1500, 60_000)) // shift 3
        assertEquals(24_000L, Backoff.computeMs(9, 5, 1500, 60_000)) // shift 4
        assertEquals(48_000L, Backoff.computeMs(10, 5, 1500, 60_000))// shift 5
        assertEquals(60_000L, Backoff.computeMs(11, 5, 1500, 60_000))// shift 6 → 96000 clamped
        assertEquals(60_000L, Backoff.computeMs(50, 5, 1500, 60_000))// deep clamp
    }

    @Test
    fun `below threshold returns base`() {
        assertEquals(1500L, Backoff.computeMs(0, 5, 1500, 60_000))
        assertEquals(1500L, Backoff.computeMs(4, 5, 1500, 60_000))
    }
}

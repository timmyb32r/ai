package com.crimobile.player

import org.junit.Assert.*
import org.junit.Test

/**
 * Regression tests for the live-player auto-reconnect backoff.
 *
 * Background: [ExoRadioPlayer.play] previously reset the retry counter on every
 * call — including the call made from the auto-retry path — so MAX_RETRIES was
 * never reached and the exponential backoff never engaged (infinite 1s retry
 * loop). [RetryController] isolates that logic so it can be tested without a
 * real ExoPlayer.
 */
class RetryControllerTest {

    @Test
    fun `delays grow exponentially then clamp at max`() {
        val r = RetryController(maxRetries = 10, baseDelayMs = 1000, maxDelayMs = 32_000)
        val delays = mutableListOf<Long>()
        while (true) {
            val d = r.nextDelayMs() ?: break
            delays.add(d)
        }
        assertEquals(
            listOf(1000L, 2000L, 4000L, 8000L, 16000L, 32000L, 32000L, 32000L, 32000L, 32000L),
            delays
        )
        assertFalse("cap exhausted", r.canRetry())
        assertEquals(10, r.retryCount)
    }

    @Test
    fun `reset allows retries again`() {
        val r = RetryController(maxRetries = 3, baseDelayMs = 1000, maxDelayMs = 32_000)
        r.nextDelayMs()
        r.nextDelayMs()
        r.nextDelayMs()
        assertFalse("exhausted after 3", r.canRetry())
        assertNull(r.nextDelayMs())

        r.reset()
        assertTrue("reset re-enables retries", r.canRetry())
        assertEquals(1000L, r.nextDelayMs())
    }

    @Test
    fun `no Long overflow at pathological call counts`() {
        // Previously the backoff in HttpSubtitleSource used an unbounded shift
        // exponent (1L shl n) which overflowed Long and produced a negative delay
        // (→ delay() became a no-op → tight retry loop). RetryController clamps.
        val r = RetryController(maxRetries = 200, baseDelayMs = 1500, maxDelayMs = 60_000)
        while (true) {
            val d = r.nextDelayMs() ?: break
            assertTrue("delay must be positive, was $d", d > 0)
            assertTrue("delay must not exceed cap, was $d", d <= 60_000)
        }
        assertEquals(200, r.retryCount)
    }

    @Test
    fun `canRetry false from start when maxRetries is zero`() {
        val r = RetryController(maxRetries = 0, baseDelayMs = 1000, maxDelayMs = 32_000)
        assertFalse(r.canRetry())
        assertNull(r.nextDelayMs())
    }

    @Test
    fun `single retry yields base delay`() {
        val r = RetryController(maxRetries = 1, baseDelayMs = 1000, maxDelayMs = 32_000)
        assertEquals(1000L, r.nextDelayMs())
        assertFalse(r.canRetry())
        assertNull(r.nextDelayMs())
    }
}

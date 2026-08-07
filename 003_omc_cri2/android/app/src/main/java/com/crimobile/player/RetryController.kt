package com.crimobile.player

/**
 * Pure, testable retry/backoff controller for the live-player auto-reconnect.
 *
 * Extracted from [ExoRadioPlayer] so the reconnect invariant can be unit-tested
 * without a real ExoPlayer. Previously [ExoRadioPlayer.play] reset the retry
 * counter on **every** call — including the call made from the auto-retry path —
 * so `MAX_RETRIES` was never reached and the exponential backoff never engaged:
 * the player retried every 1s forever (battery drain, no error screen).
 *
 * Contract:
 *  - [nextDelayMs] returns the exponential delay for the next attempt and
 *    advances the counter, or `null` when the cap is exhausted.
 *  - [reset] is called ONLY on a manual (user-initiated) play or on successful
 *    playback — never from the auto-retry path.
 *  - The shift exponent is clamped to [MAX_SAFE_SHIFT] so `1L shl n` cannot
 *    overflow `Long` for pathological call counts.
 */
class RetryController(
    private val maxRetries: Int,
    private val baseDelayMs: Long,
    private val maxDelayMs: Long
) {
    var retryCount: Int = 0
        private set

    /** True if another auto-retry attempt is allowed. */
    fun canRetry(): Boolean = retryCount < maxRetries

    /**
     * Returns the delay for the next attempt and increments the counter,
     * or `null` if no more attempts are allowed.
     */
    fun nextDelayMs(): Long? {
        if (retryCount >= maxRetries) return null
        val shift = retryCount.coerceAtMost(MAX_SAFE_SHIFT)
        val delay = (baseDelayMs * (1L shl shift)).coerceAtMost(maxDelayMs)
        retryCount++
        return delay
    }

    /** Reset on manual (user) play / successful playback. */
    fun reset() {
        retryCount = 0
    }

    private companion object {
        // 1L shl n overflows Long at n >= 63; clamp well below that.
        const val MAX_SAFE_SHIFT = 20
    }
}

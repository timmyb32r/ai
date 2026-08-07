package com.crimobile.subtitles

/**
 * Pure, testable exponential-backoff delay computation.
 *
 * Extracted from the subtitle poll loop and SSE reconnect path so the
 * Long-overflow regression can be unit-tested. Previously the poll loop used
 * `base * (1L shl (failures - threshold))` with an unbounded shift exponent;
 * once `failures - threshold >= 63`, `1L shl 63` overflowed to `Long.MIN_VALUE`,
 * the negative value slipped past `coerceAtMost`, and `delay(negative)` became
 * a no-op — turning a long server outage into a tight retry loop (100% CPU,
 * battery drain, self-DoS on recovery).
 *
 * This clamps the shift exponent to [MAX_SAFE_SHIFT] and clamps the result to
 * `[baseMs, maxMs]`, so the delay is always a positive, bounded value.
 */
object Backoff {
    private const val MAX_SAFE_SHIFT = 20

    /**
     * @param consecutiveFailures current failure count
     * @param threshold failures before backoff begins (shift = failures - threshold)
     * @param baseMs delay at shift 0 (and the minimum returned)
     * @param maxMs maximum delay (cap)
     */
    fun computeMs(
        consecutiveFailures: Int,
        threshold: Int,
        baseMs: Long,
        maxMs: Long
    ): Long {
        val shift = (consecutiveFailures - threshold).coerceIn(0, MAX_SAFE_SHIFT)
        val raw = baseMs * (1L shl shift)
        return raw.coerceIn(baseMs, maxMs)
    }
}

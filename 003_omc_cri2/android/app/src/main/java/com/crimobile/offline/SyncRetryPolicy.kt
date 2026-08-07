package com.crimobile.offline

/**
 * Pure, testable retry policy for [SyncWorker].
 *
 * Previously [SyncWorker.doWork] returned `Result.retry()` on every failure with
 * no attempt cap and no backoff, so a persistently unreachable server caused an
 * unbounded retry storm (each retry also spun up a fresh DownloadEngine +
 * OkHttpClient). This isolates the cap decision so it can be unit-tested
 * without WorkManager runtime.
 */
object SyncRetryPolicy {
    /** Maximum number of retry attempts before giving up with Result.failure(). */
    const val MAX_ATTEMPTS = 5

    /**
     * True if the worker should retry for the given [runAttemptCount]
     * (WorkManager's 1-based attempt counter; the first run is 1).
     */
    fun shouldRetry(runAttemptCount: Int): Boolean = runAttemptCount < MAX_ATTEMPTS
}

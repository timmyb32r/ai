package com.crimobile.offline

import org.junit.Assert.*
import org.junit.Test

/**
 * Regression test for the SyncWorker retry storm.
 *
 * Previously [SyncWorker.doWork] returned `Result.retry()` on every failure with
 * no cap, so a persistently unreachable server retried forever (each retry also
 * built a fresh DownloadEngine + OkHttpClient). [SyncRetryPolicy] bounds it.
 */
class SyncRetryPolicyTest {

    @Test
    fun `shouldRetry is true below the cap`() {
        assertTrue(SyncRetryPolicy.shouldRetry(1))
        assertTrue(SyncRetryPolicy.shouldRetry(4))
    }

    @Test
    fun `shouldRetry is false at and above the cap`() {
        assertFalse(SyncRetryPolicy.shouldRetry(SyncRetryPolicy.MAX_ATTEMPTS))
        assertFalse(SyncRetryPolicy.shouldRetry(SyncRetryPolicy.MAX_ATTEMPTS + 1))
        assertFalse(SyncRetryPolicy.shouldRetry(100))
    }
}

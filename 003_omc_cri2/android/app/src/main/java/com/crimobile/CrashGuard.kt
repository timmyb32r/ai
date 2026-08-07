package com.crimobile

import java.util.concurrent.atomic.AtomicBoolean

/**
 * Pure, testable guard for a global uncaught-exception handler.
 *
 * Extracted from [CrashHandler] so two invariants can be unit-tested without
 * an Android runtime:
 *
 *  1. The OS default handler is ALWAYS invoked — even if writing the crash
 *     dump itself throws an `Error` (OOM/StackOverflow from a giant stack
 *     trace). Previously [CrashHandler] caught only `Exception`, so an `Error`
 *     escaped before `defaultHandler.uncaughtException` ran and the standard
 *     "app has stopped" dialog never appeared.
 *  2. Reentrancy is bounded — if a secondary exception is thrown while handling
 *     the first, the dump step is skipped and the default handler runs directly
 *     (no infinite recursion / stack overflow).
 */
class CrashGuard {
    private val inHandler = AtomicBoolean(false)

    /**
     * @param writeDump best-effort dump writer (may throw anything; swallowed)
     * @param callDefault invokes the OS default uncaught-exception handler
     */
    fun handle(writeDump: () -> Unit, callDefault: () -> Unit) {
        if (!inHandler.compareAndSet(false, true)) {
            // Reentrant — skip the dump (it may be what's crashing) and go straight
            // to the default handler.
            callDefault()
            return
        }
        try {
            writeDump()
        } catch (_: Throwable) {
            // Swallow EVERYTHING (including Error) — the default handler must still run.
        } finally {
            inHandler.set(false)
            callDefault()
        }
    }
}

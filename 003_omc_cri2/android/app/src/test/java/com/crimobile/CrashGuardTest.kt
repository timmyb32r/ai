package com.crimobile

import org.junit.Assert.*
import org.junit.Test
import java.util.concurrent.atomic.AtomicInteger

/**
 * Regression tests for the global crash handler invariants.
 *
 * Previously [CrashHandler] caught only `Exception`, so an `Error` (OOM/
 * StackOverflow from a giant stack trace) escaped before the OS default handler
 * ran — the standard "app has stopped" dialog never appeared. [CrashGuard]
 * guarantees the default handler always runs and bounds reentrancy.
 */
class CrashGuardTest {

    @Test
    fun `default handler runs when dump succeeds`() {
        val guard = CrashGuard()
        val dumpCalls = AtomicInteger(0)
        val defaultCalls = AtomicInteger(0)

        guard.handle(
            writeDump = { dumpCalls.incrementAndGet() },
            callDefault = { defaultCalls.incrementAndGet() }
        )

        assertEquals(1, dumpCalls.get())
        assertEquals(1, defaultCalls.get())
    }

    @Test
    fun `default handler still runs when dump throws OutOfMemoryError`() {
        val guard = CrashGuard()
        val defaultCalls = AtomicInteger(0)

        guard.handle(
            writeDump = { throw OutOfMemoryError("boom") },
            callDefault = { defaultCalls.incrementAndGet() }
        )

        assertEquals("default handler must run despite Error", 1, defaultCalls.get())
    }

    @Test
    fun `default handler still runs when dump throws RuntimeException`() {
        val guard = CrashGuard()
        val defaultCalls = AtomicInteger(0)

        guard.handle(
            writeDump = { throw RuntimeException("boom") },
            callDefault = { defaultCalls.incrementAndGet() }
        )

        assertEquals(1, defaultCalls.get())
    }

    @Test
    fun `reentrant call skips the dump and calls default directly`() {
        val guard = CrashGuard()
        val dumpCalls = AtomicInteger(0)
        val defaultCalls = AtomicInteger(0)

        guard.handle(
            writeDump = {
                dumpCalls.incrementAndGet()
                // Simulate a secondary crash while handling the first.
                guard.handle(
                    writeDump = { dumpCalls.incrementAndGet() }, // must be skipped
                    callDefault = { defaultCalls.incrementAndGet() }
                )
            },
            callDefault = { defaultCalls.incrementAndGet() }
        )

        assertEquals("outer + inner default both run", 2, defaultCalls.get())
        assertEquals("outer dump ran, inner dump skipped", 1, dumpCalls.get())
    }
}

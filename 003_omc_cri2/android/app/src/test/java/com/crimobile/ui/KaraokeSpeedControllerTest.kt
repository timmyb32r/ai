package com.crimobile.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Locks in the *restoring* control law of [KaraokeSpeedController].
 *
 * Regression context: the previous law was `multiplier = position * 2`, whose
 * equilibrium for a non-advancing active word is position 0 (the TOP of the
 * screen). Once the scroll loop was made bulletproof (never dies), that latent
 * bug surfaced as "the highlighted word drifts to the top and parks there" —
 * reported by the user as "the spoken-word highlight broke".
 *
 * The corrected law has its equilibrium at [TARGET] (inside the 25%–75% reading
 * zone) and produces zero scroll at/above the target, so the word can never be
 * pushed further toward the top.
 */
class KaraokeSpeedControllerTest {

    private val controller = KaraokeSpeedController()

    // Must match KaraokeSpeedController.targetPosition.
    private val TARGET = 0.40f

    @Test
    fun `multiplier at target is zero — equilibrium`() {
        assertEquals(0.0f, controller.getMultiplier(TARGET), 0.02f)
    }

    /**
     * REGRESSION GUARD. A word at or above the target must never scroll further
     * up. If this fails, the drift-to-top bug is back.
     */
    @Test
    fun `multiplier is zero for every position at or above target`() {
        var p = 0.0f
        while (p <= TARGET + 1e-4f) {
            assertEquals(
                "multiplier at p=$p should be 0 (no upward drift above the target)",
                0.0f, controller.getMultiplier(p), 0.02f
            )
            p += 0.01f
        }
    }

    @Test
    fun `multiplier is positive and increasing below the target`() {
        // Sample strictly below the reading target (i.e. word too low on screen).
        val samples = listOf(0.5f, 0.6f, 0.75f, 0.9f, 1.0f)
        var prev = 0.0f
        for (p in samples) {
            val m = controller.getMultiplier(p)
            assertTrue("multiplier at p=$p should be > 0 to pull the word up", m > 0f)
            assertTrue("multiplier should increase with p (p=$p)", m >= prev)
            prev = m
        }
    }

    @Test
    fun `multiplier at bottom is 2_0`() {
        assertEquals(2.0f, controller.getMultiplier(1f), 0.01f)
    }

    @Test
    fun `multiplier is monotonic non-decreasing`() {
        for (i in 0..98) {
            val lower = controller.getMultiplier(i / 100f)
            val higher = controller.getMultiplier((i + 1) / 100f)
            assertTrue(
                "multiplier at ${(i + 1) / 100f} should be >= at ${i / 100f}",
                higher >= lower
            )
        }
    }

    @Test
    fun `multiplier clamps out-of-range positions`() {
        assertEquals(0.0f, controller.getMultiplier(-0.5f), 0.02f)
        assertEquals(2.0f, controller.getMultiplier(1.5f), 0.01f)
    }

    /**
     * "Never breaks again" simulation. Models the CriApp scroll loop for a
     * STATIONARY active word (index not advancing — e.g. player buffering).
     * Each tick scrolls content up by `baseSpeed * multiplier(p) * dt`, which
     * decreases the word's screen position.
     *
     * Assertions (hold for every start):
     *  - the word NEVER drifts into the top band (position stays >= 0.25), and
     *  - it settles inside the reading zone, at-or-above the target — a word
     *    below the target eases up to it; a word already at/above the target
     *    (multiplier == 0) simply holds position. It never sinks to 0.
     *
     * Under the old `position * 2` law this fails (p → 0 for every start).
     */
    @Test
    fun `stationary active word stays in the reading zone and does not drift to the top`() {
        val viewportPx = 2000f
        val baseSpeedPxPerSec = 86.4f   // matches the value seen in the field logs
        val dt = 0.016f                 // ~60 fps tick

        for (start in listOf(0.25f, 0.40f, 0.60f, 0.80f)) {
            var p = start
            var minSeen = p
            repeat(2000) {
                val px = baseSpeedPxPerSec * controller.getMultiplier(p) * dt
                p = (p - px / viewportPx).coerceIn(0f, 1f)
                if (p < minSeen) minSeen = p
            }
            // Never above the reading zone (this is the anti-drift guarantee).
            assertTrue(
                "word starting at $start drifted above the reading zone (minSeen=$minSeen)",
                minSeen >= 0.25f - 0.02f
            )
            // Settles in the zone, at-or-just-above the target — never sinks to 0.
            assertTrue(
                "word starting at $start settled outside [0.25, target] (final=$p)",
                p >= 0.25f - 0.02f && p <= TARGET + 0.05f
            )
        }
    }
}

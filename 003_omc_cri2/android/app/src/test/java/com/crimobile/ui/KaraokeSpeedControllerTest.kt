package com.crimobile.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class KaraokeSpeedControllerTest {

    private val controller = KaraokeSpeedController()

    @Test
    fun `multiplier at 50 percent is 1_0`() {
        assertEquals(1.0f, controller.getMultiplier(0.5f), 0.01f)
    }

    @Test
    fun `multiplier at 25 percent is 0_5`() {
        assertEquals(0.5f, controller.getMultiplier(0.25f), 0.01f)
    }

    @Test
    fun `multiplier at 75 percent is 1_5`() {
        assertEquals(1.5f, controller.getMultiplier(0.75f), 0.01f)
    }

    @Test
    fun `multiplier at 0 percent is 0_0`() {
        assertEquals(0.0f, controller.getMultiplier(0f), 0.01f)
    }

    @Test
    fun `multiplier at 100 percent is 2_0`() {
        assertEquals(2.0f, controller.getMultiplier(1f), 0.01f)
    }

    @Test
    fun `multiplier is monotonic`() {
        for (i in 0..98) {
            val lower = controller.getMultiplier(i / 100f)
            val higher = controller.getMultiplier((i + 1) / 100f)
            assertTrue(
                "multiplier at ${(i+1)/100f} should be >= at ${i/100f}",
                higher >= lower
            )
        }
    }

    @Test
    fun `multiplier clamps out-of-range positions`() {
        assertEquals(0.0f, controller.getMultiplier(-0.5f), 0.01f)
        assertEquals(2.0f, controller.getMultiplier(1.5f), 0.01f)
    }
}

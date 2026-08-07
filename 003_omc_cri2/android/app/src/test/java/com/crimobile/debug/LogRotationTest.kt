package com.crimobile.debug

import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File

/**
 * Regression tests for log rotation.
 *
 * Previously [DebugLogger] opened the log in append mode and never rotated, so a
 * long-running session could grow it without bound in internal storage.
 */
class LogRotationTest {

    @get:Rule
    val tmp = TemporaryFolder()

    @Test
    fun `shouldRotate is true at and above threshold`() {
        assertTrue(LogRotation.shouldRotate(5_000_000, 5_000_000))
        assertTrue(LogRotation.shouldRotate(6_000_000, 5_000_000))
    }

    @Test
    fun `shouldRotate is false below threshold`() {
        assertFalse(LogRotation.shouldRotate(4_999_999, 5_000_000))
        assertFalse(LogRotation.shouldRotate(0, 5_000_000))
    }

    @Test
    fun `shouldRotate is false when threshold is zero`() {
        assertFalse(LogRotation.shouldRotate(1_000_000, 0))
    }

    @Test
    fun `rotate renames current to archive`() {
        val dir = tmp.newFolder("logs")
        val current = File(dir, "cri_logs.txt").apply { writeText("hello") }
        val archive = File(dir, "cri_logs.txt.1")

        assertTrue(LogRotation.rotate(current, archive))
        assertFalse("current moved away", current.exists())
        assertTrue("archive created", archive.exists())
        assertEquals("hello", archive.readText())
    }

    @Test
    fun `rotate replaces an existing archive`() {
        val dir = tmp.newFolder("logs")
        val current = File(dir, "cri_logs.txt").apply { writeText("new") }
        val archive = File(dir, "cri_logs.txt.1").apply { writeText("old") }

        assertTrue(LogRotation.rotate(current, archive))
        assertEquals("old archive replaced", "new", archive.readText())
    }
}

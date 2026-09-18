package com.crimobile.offline

import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.Protocol
import okhttp3.Request
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.IOException

class ArchiveTransferTest {
    @get:Rule val tmp = TemporaryFolder()
    private fun segment(id: Int) = """{"segment_id":$id,"timeline_start_sec":${id * 3},"timeline_end_sec":${id * 3 + 3},"ts_file":"$id.ts","text_zh":"","words":[]}"""
    private fun client(answer: (Request) -> Pair<Int, String>) = OkHttpClient.Builder().addInterceptor { chain ->
        val (code, body) = answer(chain.request())
        Response.Builder().request(chain.request()).protocol(Protocol.HTTP_1_1).code(code).message("test")
            .body(body.toResponseBody()).build()
    }.build()
    private fun page(total: Int, content: String, snapshot: Boolean = true) =
        """{"total":$total,"segments":[$content]${if (snapshot) ",\"snapshot_id\":\"test-pin\",\"snapshot_expires_at\":\"2099-01-01T00:00:00Z\"" else ""}}"""

    @Test fun `snapshot token continues pagination and release is sent`() = runBlocking<Unit> {
        val requests = mutableListOf<Request>()
        val transfer = ArchiveTransfer("http://test", client { req ->
            requests.add(req)
            200 to if (req.method == "DELETE") "{}" else if (req.url.queryParameter("offset") == "0") page(2, segment(1)) else page(2, segment(2))
        })
        val result = transfer.fetchSegments(0.0, 12.0) { _, _ -> }
        assertEquals(2, result.size)
        assertEquals("test-pin", requests[1].url.queryParameter("snapshot"))
        assertEquals("1", requests[1].url.queryParameter("offset"))
        transfer.release()
        assertEquals("DELETE", requests.last().method)
        assertEquals("/api/snapshots/test-pin", requests.last().url.encodedPath)
    }

    @Test fun `legacy server without snapshot still works`() = runBlocking<Unit> {
        val transfer = ArchiveTransfer("http://test", client { 200 to page(1, segment(1), false) })
        assertEquals(1, transfer.fetchSegments(0.0, 12.0) { _, _ -> }.size)
        transfer.release()
    }

    @Test fun `oversized first and later pages halve limit without skipping or replacing pin`() = runBlocking<Unit> {
        val requests = mutableListOf<Request>()
        val transfer = ArchiveTransfer("http://test", client { req ->
            requests.add(req)
            val offset = req.url.queryParameter("offset")
            val limit = req.url.queryParameter("limit")!!.toInt()
            when {
                offset == "0" && limit > 250 -> 413 to "too large"
                offset == "0" -> 200 to page(2, segment(1))
                limit > 125 -> 413 to "too large"
                else -> 200 to page(2, segment(2))
            }
        })
        assertEquals(listOf(1, 2), transfer.fetchSegments(0.0, 12.0) { _, _ -> }.map { it.segment_id })
        assertEquals(listOf("500", "250", "250", "125"), requests.map { it.url.queryParameter("limit") })
        assertEquals(listOf("0", "0", "1", "1"), requests.map { it.url.queryParameter("offset") })
        assertEquals(listOf("create", "create", "test-pin", "test-pin"), requests.map { it.url.queryParameter("snapshot") })
    }

    @Test fun `oversized single metadata record fails after bounded retries`() = runBlocking<Unit> {
        var requests = 0
        val transfer = ArchiveTransfer("http://test", client { requests++; 413 to "too large" })
        try { transfer.fetchSegments(0.0, 12.0) { _, _ -> }; fail("must fail") } catch (_: IOException) { }
        assertEquals(9, requests) // 500,250,125,62,31,15,7,3,1
    }

    @Test fun `expired pin rejects audio before network`() = runBlocking<Unit> {
        var now = 0L
        var calls = 0
        val transfer = ArchiveTransfer("http://test", client { calls++; 200 to page(1, segment(1)) }, { now })
        val seg = transfer.fetchSegments(0.0, 12.0) { _, _ -> }.single()
        now = Long.MAX_VALUE
        try { transfer.audio(seg); fail("must fail") } catch (_: IOException) { }
        assertEquals(1, calls)
    }

    @Test fun `changed total or truncated page fails instead of skipping records`() = runBlocking<Unit> {
        val transfer = ArchiveTransfer("http://test", client { req ->
            200 to if (req.url.queryParameter("offset") == "0") page(2, segment(1)) else page(1, "")
        })
        try { transfer.fetchSegments(0.0, 12.0) { _, _ -> }; fail("must fail") } catch (_: IOException) { }
    }

    @Test fun `zero of N audio downloads fails releases pin and publishes no offline session`() = runBlocking<Unit> {
        val requests = java.util.Collections.synchronizedList(mutableListOf<Request>())
        val store = OfflineStorageManager.forRoot(tmp.newFolder())
        val engine = DownloadEngine(null, "http://test", store, client { req ->
            requests.add(req)
            when {
                req.method == "DELETE" -> 200 to "{}"
                req.url.encodedPath.startsWith("/api/segments") -> 200 to page(1, segment(1))
                else -> 404 to "gone"
            }
        })
        val result = engine.downloadRange(0.0, 12.0) { }
        assertTrue(result.isFailure)
        assertTrue(store.loadAllSessions().isEmpty())
        assertTrue(requests.any { it.method == "DELETE" })
    }

    @Test fun `empty successful audio response is an error`() = runBlocking<Unit> {
        val transfer = ArchiveTransfer("http://test", client { req ->
            200 to if (req.url.encodedPath.startsWith("/api/segments")) page(1, segment(1)) else ""
        })
        val segment = transfer.fetchSegments(0.0, 12.0) { _, _ -> }.single()
        try { transfer.audio(segment); fail("must fail") } catch (_: IOException) { }
    }

    @Test fun `partially available audio never publishes a successful shortened session`() = runBlocking<Unit> {
        val store = OfflineStorageManager.forRoot(tmp.newFolder())
        val engine = DownloadEngine(null, "http://test", store, client { req ->
            when {
                req.method == "DELETE" -> 200 to "{}"
                req.url.encodedPath.startsWith("/api/segments") -> 200 to page(2, "${segment(1)},${segment(2)}")
                req.url.encodedPath.endsWith("/1.ts") -> 200 to "audio"
                else -> 404 to "gone"
            }
        })
        val result = engine.downloadRange(0.0, 12.0) { }
        assertTrue(result.isFailure)
        assertTrue(store.loadAllSessions().isEmpty())
    }
}

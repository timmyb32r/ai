package com.crimobile.offline

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import okhttp3.OkHttpClient
import okhttp3.Request
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.Closeable
import java.io.IOException
import java.net.ServerSocket
import java.net.Socket
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

class CancellableHttpTest {
    @get:Rule val tmp = TemporaryFolder()

    private class Fixture(private val handle: (Socket, String) -> Unit) : Closeable {
        private val server = ServerSocket(0, 8, java.net.InetAddress.getLoopbackAddress())
        private val workers = Executors.newCachedThreadPool()
        private val sockets = java.util.Collections.synchronizedList(mutableListOf<Socket>())
        val url = "http://127.0.0.1:${server.localPort}"

        init {
            workers.execute {
                while (!server.isClosed) {
                    val socket = try { server.accept() } catch (_: IOException) { break }
                    sockets += socket
                    workers.execute {
                        socket.use {
                            try {
                                socket.soTimeout = 5000
                                val input = socket.getInputStream().bufferedReader()
                                val request = input.readLine() ?: return@use
                                while (!input.readLine().isNullOrEmpty()) { }
                                handle(socket, request)
                            } catch (_: IOException) { }
                            catch (_: InterruptedException) { Thread.currentThread().interrupt() }
                        }
                    }
                }
            }
        }

        override fun close() {
            server.close()
            synchronized(sockets) { sockets.forEach { it.close() } }
            workers.shutdownNow()
            check(workers.awaitTermination(2, TimeUnit.SECONDS))
        }
    }

    private fun respond(socket: Socket, body: String) {
        val bytes = body.toByteArray()
        socket.getOutputStream().apply {
            write("HTTP/1.1 200 OK\r\nContent-Length: ${bytes.size}\r\nConnection: close\r\n\r\n".toByteArray())
            write(bytes)
            flush()
        }
    }

    @Test
    fun `cancelling a stalled audio body closes socket releases pin and preserves previous session`() = runBlocking<Unit> {
        val audioStarted = CountDownLatch(1)
        val disconnected = CountDownLatch(1)
        val released = CountDownLatch(1)
        Fixture { socket, request ->
            when {
                request.startsWith("DELETE ") -> { respond(socket, "{}"); released.countDown() }
                request.contains("/api/segments/range") -> respond(socket, """{"total":1,"snapshot_id":"pin","snapshot_expires_at":"2099-01-01T00:00:00Z","segments":[{"segment_id":1,"ts_file":"1.ts","timeline_start_sec":0,"timeline_end_sec":3,"text_zh":"","words":[]}]}""")
                else -> {
                    socket.getOutputStream().apply {
                        write("HTTP/1.1 200 OK\r\nContent-Length: 188\r\nConnection: close\r\n\r\nx".toByteArray())
                        flush()
                    }
                    audioStarted.countDown()
                    if (socket.getInputStream().read() == -1) disconnected.countDown()
                }
            }
        }.use { fixture ->
            val store = OfflineStorageManager.forRoot(tmp.newFolder())
            val previous = store.createSession(0, 3)
            val marker = java.io.File(store.sessionAudioDir(previous), "000000001.ts")
            marker.writeText("previous complete audio")
            val client = OkHttpClient.Builder().readTimeout(10, TimeUnit.SECONDS).build()
            try {
                val engine = DownloadEngine(null, fixture.url, store, client)
                val job = async { engine.downloadRange(0.0, 3.0) { } }
                assertTrue(withContext(Dispatchers.IO) { audioStarted.await(3, TimeUnit.SECONDS) })
                withTimeout(3000) { job.cancelAndJoin() }
                assertTrue(released.await(1, TimeUnit.SECONDS))
                assertTrue(disconnected.await(1, TimeUnit.SECONDS))
                assertEquals("previous complete audio", marker.readText())
                assertFalse(store.sessionDir(previous).parentFile!!.listFiles()!!.any { it.name.startsWith(".download-") })
            } finally {
                client.dispatcher.executorService.shutdownNow()
                client.connectionPool.evictAll()
            }
        }
    }

    @Test
    fun `total call deadline interrupts a trickling body despite regular read progress`() = runBlocking<Unit> {
        Fixture { socket, _ ->
            socket.getOutputStream().apply {
                write("HTTP/1.1 200 OK\r\nContent-Length: 1000\r\nConnection: close\r\n\r\n".toByteArray())
                repeat(1000) { write('x'.code); flush(); Thread.sleep(20) }
            }
        }.use { fixture ->
            val client = OkHttpClient.Builder().readTimeout(1, TimeUnit.SECONDS)
                .callTimeout(250, TimeUnit.MILLISECONDS).build()
            try {
                val began = System.nanoTime()
                try {
                    client.readCancellable(Request.Builder().url(fixture.url).build()) { it.body!!.bytes() }
                    fail("Trickling body must time out")
                } catch (_: IOException) { }
                assertTrue(TimeUnit.NANOSECONDS.toMillis(System.nanoTime() - began) < 2000)
            } finally {
                client.dispatcher.executorService.shutdownNow()
                client.connectionPool.evictAll()
            }
        }
    }
}

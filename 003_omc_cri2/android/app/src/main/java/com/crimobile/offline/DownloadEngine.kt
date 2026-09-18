package com.crimobile.offline

import android.content.Context
import com.crimobile.model.SubtitleSegment
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONObject
import java.io.IOException
import java.util.concurrent.TimeUnit
import com.crimobile.debug.DebugLogger

data class ArchiveInfo(
    val oldestStartSec: Double = 0.0,
    val newestEndSec: Double = 0.0,
    val segmentsTotal: Long = 0
)

data class DownloadProgress(
    val totalSegments: Int = 0,
    val downloadedSegments: Int = 0,
    val currentAction: String = "",
    val isRunning: Boolean = false,
    val error: String? = null
)

/**
 * Downloads metadata + audio for a time range from the server.
 *
 * Flow:
 *  1. fetchArchiveInfo() → /api/status  → validates range
 *  2. downloadRange()   → /api/segments/range + /hls/{ts_file}
 *
 * Cancellation is cooperative via coroutine isActive checks.
 *
 * Uses a process-wide shared [OkHttpClient] (companion object) so the
 * dispatcher thread pool and connection pool are not leaked per instance —
 * DownloadEngine is created fresh for each download / SyncWorker retry.
 */
class DownloadEngine(
    context: Context?,
    private val serverUrl: String,
    private val storageManager: OfflineStorageManager,
    private val client: OkHttpClient = sharedClient
) {

    /** Fetch the server's archive time bounds. */
    suspend fun fetchArchiveInfo(): ArchiveInfo = withContext(Dispatchers.IO) {
        val request = Request.Builder()
            .url("$serverUrl/api/status")
            .header("Accept", "application/json")
            .build()

        client.readCancellable(request) { response ->
            if (!response.isSuccessful) throw IOException("Archive status HTTP ${response.code}")
            val body = response.body?.string() ?: throw IOException("Empty status response")
            val json = JSONObject(body)

            ArchiveInfo(
                oldestStartSec = json.optDouble("oldest_segment_start_sec", 0.0),
                newestEndSec = json.optDouble("newest_segment_end_sec", 0.0),
                segmentsTotal = json.optLong("segments_total", 0)
            )
        }
    }

    /**
     * Downloads all segments whose timeline overlaps [startSec, endSec].
     *
     * @param startSec  Unix epoch seconds for start of window
     * @param endSec    Unix epoch seconds for end of window
     * @param onProgress Callback on the main thread for UI updates
     */
    suspend fun downloadRange(
        startSec: Double,
        endSec: Double,
        onProgress: suspend (DownloadProgress) -> Unit
    ): Result<Unit> = withContext(Dispatchers.IO) {
        val transfer = ArchiveTransfer(serverUrl, client)
        var stagingSession: String? = null
        try {
            onProgress(DownloadProgress(currentAction = "Fetching segment list…", isRunning = true))

            // 1. Fetch paginated metadata
            val allSegments = transfer.fetchSegments(startSec, endSec) { page, total ->
                onProgress(DownloadProgress(
                    totalSegments = total,
                    downloadedSegments = 0,
                    currentAction = "Indexed $page segments…",
                    isRunning = true
                ))
            }

            currentCoroutineContext().ensureActive()
            if (allSegments.isEmpty()) {
                throw IOException("No segments found for the requested time range")
            }

            DebugLogger.i(TAG, "downloadRange: ${allSegments.size} segments to download")

            // Create session directory before downloading
            val sessionId = storageManager.createDownloadSession()
            stagingSession = sessionId

            // 2. Download .ts files in parallel batches (10 concurrent)
            val totalSize = allSegments.size
            var downloadedCount = 0
            // Only segments whose audio was actually saved make it into the index,
            // so the index never advertises segments with no audio (silent seek targets).
            val savedSegments = mutableListOf<SubtitleSegment>()

            allSegments.chunked(CONCURRENT_DOWNLOADS).forEach { batch ->
                currentCoroutineContext().ensureActive()
                transfer.checkLease()

                val audio = coroutineScope {
                    batch.map { segment -> async { downloadTsFile(transfer, segment) } }.awaitAll()
                }
                audio.forEachIndexed { i, result ->
                    val segment = batch[i]
                    storageManager.saveSegment(segment, result, sessionId)
                    savedSegments.add(segment)
                    downloadedCount++
                }

                onProgress(DownloadProgress(
                    totalSegments = totalSize,
                    downloadedSegments = downloadedCount,
                    currentAction = "Downloading $downloadedCount/$totalSize segments…",
                    isRunning = true
                ))
            }

            // Write the index under the shared storage lock (concurrent reads safe)
            // and only for segments we actually saved audio for.
            storageManager.writeSegmentIndex(sessionId, savedSegments)
            storageManager.invalidateCache(sessionId) // still delete old cache
            if (storageManager.concatAudioFiles(sessionId) == null) throw IOException("Could not assemble downloaded audio")
            currentCoroutineContext().ensureActive()
            transfer.checkLease()
            storageManager.publishDownload(sessionId, startSec.toLong(), (endSec - startSec).toInt(), downloadedCount)
            stagingSession = null

            onProgress(DownloadProgress(
                totalSegments = totalSize,
                downloadedSegments = downloadedCount,
                currentAction = "Complete: $downloadedCount segments saved",
                isRunning = false
            ))
            DebugLogger.i(TAG, "downloadRange complete: $downloadedCount/$totalSize segments")
            Result.success(Unit)
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            DebugLogger.e(TAG, "downloadRange failed: ${e.message}", e)
            onProgress(DownloadProgress(
                isRunning = false,
                error = e.message ?: "Download failed"
            ))
            Result.failure(e)
        } finally {
            withContext(NonCancellable + Dispatchers.IO) {
                stagingSession?.let { storageManager.discardDownload(it) }
                transfer.release()
            }
        }
    }

    // ── Internal ───────────────────────────────────────────────────────

    /**
     * Downloads a single .ts audio file. Retries transient failures so a single
     * HTTP blip does not leave a segment without audio until the next daily sync.
     */
    private suspend fun downloadTsFile(transfer: ArchiveTransfer, segment: SubtitleSegment): ByteArray {
        val tsFile = segment.ts_file
        var lastFailure: IOException? = null
        for (attempt in 1..TS_DOWNLOAD_ATTEMPTS) {
            currentCoroutineContext().ensureActive()
            transfer.checkLease()
            try {
                return transfer.audio(segment)
            } catch (e: IOException) {
                lastFailure = e
                DebugLogger.w(TAG, "Failed to download $tsFile (attempt $attempt): ${e.message}")
            }
            if (attempt < TS_DOWNLOAD_ATTEMPTS) delay(TS_DOWNLOAD_RETRY_MS)
        }
        throw IOException("Incomplete download: $tsFile unavailable after $TS_DOWNLOAD_ATTEMPTS attempts", lastFailure)
    }

    companion object {
        private const val TAG = "CRIRadio:download"
        private const val CONCURRENT_DOWNLOADS = 10
        private const val TS_DOWNLOAD_ATTEMPTS = 3
        private const val TS_DOWNLOAD_RETRY_MS = 1000L

        // Process-wide shared client — one dispatcher pool + one connection pool
        // instead of one per DownloadEngine instance (each download / sync retry
        // previously constructed its own and never shut it down → thread/conn leak).
        private val sharedClient = OkHttpClient.Builder()
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(30, TimeUnit.SECONDS)
            .retryOnConnectionFailure(true)
            .build()
    }
}

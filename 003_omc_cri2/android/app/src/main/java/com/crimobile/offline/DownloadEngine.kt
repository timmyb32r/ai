package com.crimobile.offline

import android.content.Context
import android.util.Log
import com.crimobile.model.SubtitleSegment
import com.crimobile.model.WordEntry
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.isActive
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException
import java.util.concurrent.TimeUnit

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
 */
class DownloadEngine(
    private val context: Context,
    private val serverUrl: String,
    private val storageManager: OfflineStorageManager
) {
    private val client = OkHttpClient.Builder()
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(30, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()

    /** Fetch the server's archive time bounds. */
    suspend fun fetchArchiveInfo(): ArchiveInfo = withContext(Dispatchers.IO) {
        val request = Request.Builder()
            .url("$serverUrl/api/status")
            .header("Accept", "application/json")
            .build()

        val response = client.newCall(request).execute()
        val body = response.body?.string() ?: throw IOException("Empty status response")
        val json = JSONObject(body)

        ArchiveInfo(
            oldestStartSec = json.optDouble("oldest_segment_start_sec", 0.0),
            newestEndSec = json.optDouble("newest_segment_end_sec", 0.0),
            segmentsTotal = json.optLong("segments_total", 0)
        )
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
        try {
            onProgress(DownloadProgress(currentAction = "Fetching segment list…", isRunning = true))

            // 1. Fetch paginated metadata
            val allSegments = fetchAllSegments(startSec, endSec) { page, total ->
                onProgress(DownloadProgress(
                    totalSegments = total,
                    downloadedSegments = 0,
                    currentAction = "Indexed $page segments…",
                    isRunning = true
                ))
            }

            if (!isActive) return@withContext Result.failure(Exception("Cancelled"))
            if (allSegments.isEmpty()) {
                return@withContext Result.failure(Exception("No segments found for the requested time range"))
            }

            Log.i(TAG, "downloadRange: ${allSegments.size} segments to download")

            // Create session directory before downloading
            val sessionId = storageManager.createSession(startSec.toLong(), (endSec - startSec).toInt())

            // 2. Download .ts files in parallel batches (10 concurrent)
            val totalSize = allSegments.size
            var downloadedCount = 0

            allSegments.chunked(CONCURRENT_DOWNLOADS).forEach { batch ->
                if (!isActive) return@withContext Result.failure(Exception("Cancelled"))

                batch.map { segment ->
                    async {
                        downloadTsFile(segment)
                    }
                }.awaitAll().forEachIndexed { i, result ->
                    if (result != null) {
                        val segment = batch[i]
                        storageManager.saveSegment(segment, result, sessionId)
                        downloadedCount++
                    }
                }

                onProgress(DownloadProgress(
                    totalSegments = totalSize,
                    downloadedSegments = downloadedCount,
                    currentAction = "Downloading $downloadedCount/$totalSize segments…",
                    isRunning = true
                ))
            }

            onProgress(DownloadProgress(
                totalSegments = totalSize,
                downloadedSegments = downloadedCount,
                currentAction = "Complete: $downloadedCount segments saved",
                isRunning = false
            ))

            // Update session index with final segment count
            val sessions = storageManager.loadAllSessions().toMutableList()
            val startSecL = startSec.toLong()
            val durSec = (endSec - startSec).toInt()
            sessions.removeAll { it.startSec == startSecL && it.durationSec == durSec }
            sessions.add(OfflineStorageManager.SessionMeta(
                startSec = startSecL,
                durationSec = durSec,
                segmentCount = downloadedCount,
                createdAt = System.currentTimeMillis()
            ))
            storageManager.writeSessionsIndex(sessions)

            Log.i(TAG, "downloadRange complete: $downloadedCount/$totalSize segments")
            Result.success(Unit)
        } catch (e: Exception) {
            Log.e(TAG, "downloadRange failed: ${e.message}", e)
            onProgress(DownloadProgress(
                isRunning = false,
                error = e.message ?: "Download failed"
            ))
            Result.failure(e)
        }
    }

    // ── Internal ───────────────────────────────────────────────────────

    /**
     * Fetches all segment metadata for the given time range using pagination.
     */
    private suspend fun fetchAllSegments(
        startSec: Double,
        endSec: Double,
        onPage: suspend (Int, Int) -> Unit
    ): List<SubtitleSegment> {
        val allSegments = mutableListOf<SubtitleSegment>()
        var offset = 0

        while (currentCoroutineContext().isActive) {
            val url = "$serverUrl/api/segments/range" +
                "?start_sec=$startSec&end_sec=$endSec" +
                "&limit=$PAGE_SIZE&offset=$offset"

            val request = Request.Builder().url(url).header("Accept", "application/json").build()
            val response = client.newCall(request).execute()
            val body = response.body?.string() ?: break
            val json = JSONObject(body)

            val segmentsArr = json.getJSONArray("segments")
            val total = json.optInt("total", 0)

            for (i in 0 until segmentsArr.length()) {
                // The segment object may be wrapped or direct — handle both
                val item = segmentsArr.optJSONObject(i) ?: continue
                // If the server returns the segment directly (not wrapped in a "segment" key),
                // parse it directly. Otherwise look for a "segment" key (SSE-like wrapper).
                val segmentObj = if (item.has("segment")) item.getJSONObject("segment") else item
                allSegments.add(parseSegment(segmentObj))
            }

            onPage(allSegments.size, total)

            offset += PAGE_SIZE
            if (offset >= total) break
        }

        return allSegments
    }

    /**
     * Downloads a single .ts audio file.
     */
    private suspend fun downloadTsFile(segment: SubtitleSegment): ByteArray? {
        return try {
            val tsFile = segment.ts_file
            val url = "$serverUrl/hls/$tsFile"
            val request = Request.Builder().url(url).build()
            val response = client.newCall(request).execute()
            if (response.isSuccessful) {
                response.body?.bytes()
            } else {
                Log.w(TAG, "HTTP ${response.code} for $tsFile")
                null
            }
        } catch (e: Exception) {
            Log.w(TAG, "Failed to download ${segment.ts_file}: ${e.message}")
            null
        }
    }

    private fun parseSegment(obj: JSONObject): SubtitleSegment {
        val wordsArr = obj.optJSONArray("words") ?: JSONArray()
        val words = (0 until wordsArr.length()).map { i ->
            val w = wordsArr.getJSONObject(i)
            WordEntry(
                text = w.optString("text", ""),
                char_start = w.optInt("char_start", 0),
                char_end = w.optInt("char_end", 0),
                start_sec = w.optDouble("start_sec", 0.0),
                end_sec = w.optDouble("end_sec", 0.0),
                pinyin = w.optString("pinyin", ""),
                translation = w.optString("translation", "")
            )
        }
        return SubtitleSegment(
            segment_id = obj.optInt("segment_id", 0),
            timeline_start_sec = obj.optDouble("timeline_start_sec", 0.0),
            timeline_end_sec = obj.optDouble("timeline_end_sec", 0.0),
            ts_file = obj.optString("ts_file", ""),
            text_zh = obj.optString("text_zh", ""),
            text_pinyin = obj.optString("text_pinyin", ""),
            text_en = obj.optString("text_en", ""),
            words = words
        )
    }

    companion object {
        private const val TAG = "CRIRadio:download"
        private const val PAGE_SIZE = 500
        private const val CONCURRENT_DOWNLOADS = 10
    }
}

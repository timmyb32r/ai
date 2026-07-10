package com.crimobile.subtitles

import android.util.Log
import com.crimobile.model.ConnectionStatus
import com.crimobile.model.SegmentMeta
import com.crimobile.model.SubtitleSegment
import com.crimobile.model.toMeta
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONObject
import java.util.concurrent.TimeUnit

private const val HTTP_TAG = "CRIRadio:http"

/**
 * Pull-based subtitle source that polls the HLS playlist and fetches
 * per-segment metadata via HTTP. Metadata follows audio 1:1 over the
 * same pull channel — desync is structurally impossible.
 *
 * Poll interval tracks the HLS segment duration (default ~3 s); the
 * poll loop runs at ~1× that interval.
 */
class HttpSubtitleSource(
    private val pollIntervalMs: Long = 1500L
) : SubtitleSource {

    private val client = OkHttpClient.Builder()
        .connectTimeout(5, TimeUnit.SECONDS)
        .readTimeout(10, TimeUnit.SECONDS)
        .retryOnConnectionFailure(true)
        .build()

    private val scope = CoroutineScope(Dispatchers.IO)
    private var pollJob: Job? = null

    private val _segments = MutableStateFlow<List<SubtitleSegment>>(emptyList())
    override val segments: StateFlow<List<SubtitleSegment>> = _segments.asStateFlow()

    private val _connected = MutableStateFlow(ConnectionStatus.DISCONNECTED)
    override val connected: StateFlow<ConnectionStatus> = _connected.asStateFlow()

    private val _segmentsMeta = MutableStateFlow<List<SegmentMeta>>(emptyList())
    override val segmentsMeta: StateFlow<List<SegmentMeta>> = _segmentsMeta.asStateFlow()

    private val segmentMap = linkedMapOf<Int, SubtitleSegment>() // insertion-ordered
    private val lock = Any()
    private val seenIds = mutableSetOf<Int>()

    // Regex: matches filenames like "000000123.ts" and captures the numeric ID
    private val tsFilePattern = Regex("^(\\d{9})\\.ts$")

    override fun connect(serverUrl: String) {
        Log.i(HTTP_TAG, "connecting to $serverUrl (poll=${pollIntervalMs}ms)")
        _connected.value = ConnectionStatus.CONNECTING
        seenIds.clear()

        pollJob?.cancel()
        pollJob = scope.launch {
            var consecutiveFailures = 0
            val maxConsecutiveFailures = 5

            while (isActive) {
                try {
                    val success = pollOnce(serverUrl)
                    if (success) {
                        consecutiveFailures = 0
                        if (_connected.value != ConnectionStatus.CONNECTED) {
                            _connected.value = ConnectionStatus.CONNECTED
                        }
                    } else {
                        consecutiveFailures++
                        if (consecutiveFailures >= maxConsecutiveFailures) {
                            _connected.value = ConnectionStatus.DISCONNECTED
                        }
                        Log.w(HTTP_TAG, "poll failed ($consecutiveFailures/$maxConsecutiveFailures)")
                    }
                } catch (e: Exception) {
                    consecutiveFailures++
                    if (consecutiveFailures >= maxConsecutiveFailures) {
                        _connected.value = ConnectionStatus.DISCONNECTED
                    }
                    Log.w(HTTP_TAG, "poll error: ${e.message} ($consecutiveFailures/$maxConsecutiveFailures)")
                }
                delay(pollIntervalMs)
            }
        }
    }

    /**
     * Cold-start fast path: fetch the last [n] segments in a single HTTP request
     * via GET /api/segments/batch?last=N.  Populates segmentMap and emits
     * immediately — no playlist fetch, no per-segment requests.
     *
     * Called from CriViewModel.Play handler BEFORE player.play(url) so that
     * text appears on screen before audio starts.
     */
    suspend fun fetchInitial(serverUrl: String, n: Int = 3, lite: Boolean = true): Boolean {
        val liteParam = if (lite) "&lite=true" else ""
        val batchUrl = "$serverUrl/api/segments/batch?last=$n$liteParam"
        val jsonBody = withContext(Dispatchers.IO) {
            fetchUrl(batchUrl)
        } ?: return false

        return try {
            val root = org.json.JSONObject(jsonBody)
            val arr = root.getJSONArray("segments")
            val segments = mutableListOf<SubtitleSegment>()
            for (i in 0 until arr.length()) {
                val segment = SubtitleParser.parseSegment(arr.getJSONObject(i))
                synchronized(lock) {
                    segmentMap[segment.segment_id] = segment
                    // Lite segments: don't mark as seen so background poll
                    // re-fetches them with full dictionary data.
                    if (!lite) seenIds.add(segment.segment_id)
                }
                segments.add(segment)
            }
            if (segments.isNotEmpty()) {
                _connected.value = ConnectionStatus.CONNECTED
                synchronized(lock) {
                    val sorted = segmentMap.values.sortedBy { it.timeline_start_sec }
                    _segments.value = sorted
                    _segmentsMeta.value = sorted.map { it.toMeta() }
                }
                Log.i(HTTP_TAG, "fetchInitial: ${segments.size} segments via bulk (lite=$lite), total=${segmentMap.size}")
            }
            true
        } catch (e: Exception) {
            Log.w(HTTP_TAG, "fetchInitial failed: ${e.message}")
            false
        }
    }

    /**
     * One poll cycle: fetch playlist → extract unseen .ts IDs → fetch metadata for each.
     * Returns true if the playlist fetch succeeded (even if no new segments).
     */
    private suspend fun pollOnce(serverUrl: String): Boolean {
        // 1. Fetch playlist.m3u8
        val playlistUrl = "$serverUrl/hls/playlist.m3u8"
        val playlistBody = fetchUrl(playlistUrl) ?: return false

        // 2. Extract .ts filenames and their segment IDs (chronological: oldest → newest)
        val tsIds = parseTsIds(playlistBody)
        if (tsIds.isEmpty()) {
            Log.d(HTTP_TAG, "playlist has no .ts entries")
            return true // empty playlist is valid, not a failure
        }

        // 3. Bound the work to a recent TAIL of the playlist.
        //    playlist.m3u8 can list the entire archived session (600–700 entries).
        //    Fetching all of them in a burst floods the main thread with GC/recompose
        //    work, starves subtitle sync (activeWord never resolves) and misaligns the
        //    delay-seek window. The newest WINDOW_SIZE segments always cover the
        //    live/delay-seek band we actually play.
        val tail = if (tsIds.size > WINDOW_SIZE) {
            tsIds.subList(tsIds.size - WINDOW_SIZE, tsIds.size)
        } else {
            tsIds
        }

        // 4. Within the tail, find IDs we haven't fetched yet
        val newIds = synchronized(lock) {
            tail.filter { it !in seenIds }
        }

        if (newIds.isEmpty()) return true

        Log.d(HTTP_TAG, "playlist has ${tsIds.size} .ts entries, fetching ${newIds.size} new (tail-bounded to $WINDOW_SIZE)")

        // 5. Fetch metadata concurrently, newest segments first.
        //
        //    Cold-start strategy (first poll — seenIds was cleared):
        //      Fetch only FIRST_CHUNK_SIZE segments (live edge) for instant
        //      display, emit immediately, then backfill the rest in larger
        //      concurrent batches without shifting visible text.
        //
        //    Newest-first means older segments are prepended to the sorted
        //    list — since the viewport tracks the live edge (end of list),
        //    items inserted at the beginning don't shift the visible area.
        val idsToFetch = newIds.sortedByDescending { it } // newest first
        var fetched = 0
        var isFirstEmit = true

        // Chunk sizes: tiny first batch for instant display, then larger.
        var remaining = idsToFetch
        while (remaining.isNotEmpty()) {
            val batchSize = if (isFirstEmit) FIRST_CHUNK_SIZE else CONCURRENT_FETCHES
            val chunk = remaining.take(batchSize)
            remaining = remaining.drop(batchSize)

            // Fetch this batch concurrently.
            val results = coroutineScope {
                chunk.map { id ->
                    async {
                        val metadataUrl = "$serverUrl/api/metadata/${segmentIdToFilename(id)}"
                        val jsonBody = fetchUrl(metadataUrl)
                        if (jsonBody != null) {
                            try {
                                val json = JSONObject(jsonBody)
                                val segment = SubtitleParser.parseSegment(json)
                                synchronized(lock) {
                                    segmentMap[segment.segment_id] = segment
                                    seenIds.add(segment.segment_id)
                                    while (segmentMap.size > 200) {
                                        val iterator = segmentMap.iterator()
                                        if (iterator.hasNext()) {
                                            val (oldestId, _) = iterator.next()
                                            iterator.remove()
                                            seenIds.remove(oldestId)
                                        }
                                    }
                                }
                                segment
                            } catch (e: Exception) {
                                Log.w(HTTP_TAG, "parse error for id=$id: ${e.message}")
                                null
                            }
                        } else null
                    }
                }.awaitAll()
            }

            val batchFetched = results.count { it != null }
            fetched += batchFetched

            // Emit immediately after the first batch — UI gets live-edge text
            // in one concurrent HTTP round-trip (~30ms).  Subsequent batches
            // are spaced by BACKFILL_DELAY_MS to avoid GC storms (parsing 100
            // SubtitleSegment objects simultaneously → 24MB GC → UI jank + audio glitch).
            if (!isFirstEmit) {
                kotlinx.coroutines.delay(BACKFILL_DELAY_MS)
            }
            if (batchFetched > 0) {
                synchronized(lock) {
                    val segments = segmentMap.values.sortedBy { it.timeline_start_sec }
                    _segments.value = segments
                    _segmentsMeta.value = segments.map { it.toMeta() }
                }
                if (isFirstEmit) {
                    isFirstEmit = false
                    Log.i(HTTP_TAG, "first batch: $batchFetched segments (live edge) in ~${batchSize}RTT — UI visible now")
                }
            }
        }

        if (fetched > 0) {
            Log.i(HTTP_TAG, "fetched $fetched new segments, total=${segmentMap.size}")
        }
        return true
    }

    /** Fetch a URL and return its body as a string, or null on failure. */
    private fun fetchUrl(url: String): String? {
        return try {
            val request = Request.Builder().url(url).build()
            val response = client.newCall(request).execute()
            if (response.isSuccessful) {
                response.body?.string()
            } else {
                Log.w(HTTP_TAG, "HTTP ${response.code} for $url")
                response.close()
                null
            }
        } catch (e: Exception) {
            Log.w(HTTP_TAG, "fetch failed $url: ${e.message}")
            null
        }
    }

    /** Extract segment IDs from an M3U8 playlist body (e.g. "000000123.ts" → 123). */
    private fun parseTsIds(playlistBody: String): List<Int> {
        return playlistBody.lines()
            .mapNotNull { line ->
                val trimmed = line.trim()
                tsFilePattern.matchEntire(trimmed)?.let { match ->
                    match.groupValues[1].toIntOrNull()
                }
            }
    }

    override fun disconnect() {
        Log.i(HTTP_TAG, "disconnect total_segments=${segmentMap.size}")
        pollJob?.cancel()
        pollJob = null
        _connected.value = ConnectionStatus.DISCONNECTED
        synchronized(lock) {
            segmentMap.clear()
            seenIds.clear()
            _segments.value = emptyList()
            _segmentsMeta.value = emptyList()
        }
    }

    companion object {
        /**
         * Max number of most-recent playlist segments to pull. Bounds the initial
         * fetch so a long archived playlist (600+ entries) never floods the app.
         * ~100 segments × ~3 s ≈ 5 min — comfortably covers the delay-seek band.
         */
        private const val WINDOW_SIZE = 100

        /** Segments to fetch in the very first batch — just enough to fill one screen. */
        private const val FIRST_CHUNK_SIZE = 3

        /** Max concurrent metadata fetches per batch (used after the first batch). */
        private const val CONCURRENT_FETCHES = 10

        /** Delay between backfill batch emissions — prevents GC storms. */
        private const val BACKFILL_DELAY_MS = 80L

        /** Zero-padded 9-digit filename, e.g. segment ID 123 → "000000123.json". */
        fun segmentIdToFilename(id: Int): String {
            return id.toString().padStart(9, '0') + ".json"
        }
    }
}

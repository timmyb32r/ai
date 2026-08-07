package com.crimobile.subtitles

import com.crimobile.model.ConnectionStatus
import com.crimobile.model.SegmentMeta
import com.crimobile.model.SubtitleSegment
import com.crimobile.model.toMeta
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.sse.EventSource
import okhttp3.sse.EventSourceListener
import okhttp3.sse.EventSources
import org.json.JSONObject
import java.util.concurrent.TimeUnit
import com.crimobile.debug.DebugLogger

private const val SSE_TAG = "CRIRadio:sse"
private const val SSE_RECONNECT_BASE_MS = 2_000L
private const val SSE_RECONNECT_MAX_MS = 60_000L

class SseSubtitleSource : SubtitleSource {

    private val client = OkHttpClient.Builder()
        .connectTimeout(5, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.MILLISECONDS) // no read timeout for SSE
        .retryOnConnectionFailure(true)
        .build()

    private val scope = CoroutineScope(Dispatchers.IO)
    private var eventSource: EventSource? = null
    private var currentUrl: String? = null
    @Volatile private var intentionallyClosed = false
    private var reconnectAttempts = 0

    private val _segments = MutableStateFlow<List<SubtitleSegment>>(emptyList())
    override val segments: StateFlow<List<SubtitleSegment>> = _segments.asStateFlow()

    private val _connected = MutableStateFlow(ConnectionStatus.DISCONNECTED)
    override val connected: StateFlow<ConnectionStatus> = _connected.asStateFlow()

    private val _segmentsMeta = MutableStateFlow<List<SegmentMeta>>(emptyList())
    override val segmentsMeta: StateFlow<List<SegmentMeta>> = _segmentsMeta.asStateFlow()

    private val segmentMap = linkedMapOf<Int, SubtitleSegment>() // insertion-ordered
    private val lock = Any()

    override fun connect(serverUrl: String) {
        DebugLogger.i(SSE_TAG, "connecting to $serverUrl/api/subtitles")
        currentUrl = serverUrl
        intentionallyClosed = false
        _connected.value = ConnectionStatus.CONNECTING

        val request = Request.Builder()
            .url("$serverUrl/api/subtitles")
            .header("Accept", "text/event-stream")
            .build()

        eventSource?.cancel()
        eventSource = EventSources.createFactory(client).newEventSource(request, object : EventSourceListener() {
            override fun onOpen(eventSource: EventSource, response: Response) {
                DebugLogger.i(SSE_TAG, "connected")
                reconnectAttempts = 0
                _connected.value = ConnectionStatus.CONNECTED
            }

            override fun onEvent(eventSource: EventSource, id: String?, type: String?, data: String) {
                when (type) {
                    "sync" -> { DebugLogger.d(SSE_TAG, "sync received"); handleSync(data) }
                    "segment" -> { /* batched: no per-segment log */ handleSegment(data) }
                    else -> DebugLogger.w(SSE_TAG, "unknown event type=$type")
                }
            }

            override fun onFailure(eventSource: EventSource, t: Throwable?, response: Response?) {
                // The listener owns the response body on HTTP-failure connects; close it
                // so the connection is returned to the pool instead of leaking per failure.
                runCatching { response?.close() }
                DebugLogger.w(SSE_TAG, "connection failed err=${t?.message} code=${response?.code}")
                _connected.value = ConnectionStatus.DISCONNECTED
                scheduleReconnect()
            }

            override fun onClosed(eventSource: EventSource) {
                DebugLogger.i(SSE_TAG, "closed segments=${segmentMap.size}")
                _connected.value = ConnectionStatus.DISCONNECTED
                scheduleReconnect()
            }
        })
    }

    /**
     * Reconnect with exponential backoff. Without this, any transient network
     * blip (WiFi flap, TCP RST) left subtitles disconnected forever while HLS
     * audio kept playing — breaking the audio/subtitle lockstep invariant.
     * Skipped when [disconnect] was called intentionally.
     */
    private fun scheduleReconnect() {
        if (intentionallyClosed) return
        val url = currentUrl ?: return
        reconnectAttempts++
        val delayMs = Backoff.computeMs(
            reconnectAttempts, 0,
            SSE_RECONNECT_BASE_MS, SSE_RECONNECT_MAX_MS
        )
        DebugLogger.i(SSE_TAG, "scheduling reconnect #$reconnectAttempts in ${delayMs}ms")
        scope.launch {
            delay(delayMs)
            if (!intentionallyClosed) connect(url)
        }
    }

    private fun handleSync(data: String) {
        // Sync event sets baseline timeline — we use segment timestamps directly
    }

    private var segmentLogCount = 0

    private fun handleSegment(data: String) {
        try {
            val json = JSONObject(data)
            val segmentJson = json.getJSONObject("segment")
            val segment = SubtitleParser.parseSegment(segmentJson)

            synchronized(lock) {
                segmentMap[segment.segment_id] = segment
                // Cap at 200 entries — remove oldest (first inserted)
                while (segmentMap.size > 200) {
                    val iterator = segmentMap.iterator()
                    if (iterator.hasNext()) {
                        iterator.next()
                        iterator.remove()
                    }
                }
                val segments = segmentMap.values.sortedBy { it.timeline_start_sec }
                _segments.value = segments
                _segmentsMeta.value = segments.map { it.toMeta() }
            }

            // Log every segment arrival with full metadata
            segmentLogCount++
            val wordSample = segment.words.take(3).joinToString("|") { "${it.text}@${it.start_sec}-${it.end_sec}" }
            DebugLogger.i(SSE_TAG, "seg#${segmentLogCount} id=${segment.segment_id} " +
                "tl=[${segment.timeline_start_sec}-${segment.timeline_end_sec}] " +
                "text=${segment.text_zh.take(40)} " +
                "words=${segment.words.size} sample=[$wordSample]")
        } catch (e: Exception) {
            DebugLogger.w(SSE_TAG, "parse error: ${e.message}")
        }
    }

    override fun disconnect() {
        DebugLogger.i(SSE_TAG, "disconnect total_segments=${segmentMap.size}")
        intentionallyClosed = true
        eventSource?.cancel()
        eventSource = null
        _connected.value = ConnectionStatus.DISCONNECTED
        scope.cancel() // cancel any pending reconnect
        synchronized(lock) {
            segmentMap.clear()
            _segments.value = emptyList()
            _segmentsMeta.value = emptyList()
        }
    }
}

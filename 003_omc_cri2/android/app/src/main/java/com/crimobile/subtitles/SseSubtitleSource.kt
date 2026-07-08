package com.crimobile.subtitles

import android.util.Log
import com.crimobile.model.ConnectionStatus
import com.crimobile.model.SubtitleSegment
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
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

private const val SSE_TAG = "CRIRadio:sse"

class SseSubtitleSource : SubtitleSource {

    private val client = OkHttpClient.Builder()
        .connectTimeout(5, TimeUnit.SECONDS)
        .readTimeout(0, TimeUnit.MILLISECONDS) // no read timeout for SSE
        .retryOnConnectionFailure(true)
        .build()

    private val scope = CoroutineScope(Dispatchers.IO)
    private var eventSource: EventSource? = null

    private val _segments = MutableStateFlow<List<SubtitleSegment>>(emptyList())
    override val segments: StateFlow<List<SubtitleSegment>> = _segments.asStateFlow()

    private val _connected = MutableStateFlow(ConnectionStatus.DISCONNECTED)
    override val connected: StateFlow<ConnectionStatus> = _connected.asStateFlow()

    private val segmentMap = linkedMapOf<Int, SubtitleSegment>() // insertion-ordered
    private val lock = Any()

    override fun connect(serverUrl: String) {
        Log.i(SSE_TAG, "connecting to $serverUrl/api/subtitles")
        _connected.value = ConnectionStatus.CONNECTING

        val request = Request.Builder()
            .url("$serverUrl/api/subtitles")
            .header("Accept", "text/event-stream")
            .build()

        eventSource?.cancel()
        eventSource = EventSources.createFactory(client).newEventSource(request, object : EventSourceListener() {
            override fun onOpen(eventSource: EventSource, response: Response) {
                Log.i(SSE_TAG, "connected")
                _connected.value = ConnectionStatus.CONNECTED
            }

            override fun onEvent(eventSource: EventSource, id: String?, type: String?, data: String) {
                when (type) {
                    "sync" -> { Log.d(SSE_TAG, "sync received"); handleSync(data) }
                    "segment" -> { /* batched: no per-segment log */ handleSegment(data) }
                    else -> Log.w(SSE_TAG, "unknown event type=$type")
                }
            }

            override fun onFailure(eventSource: EventSource, t: Throwable?, response: Response?) {
                Log.w(SSE_TAG, "connection failed err=${t?.message} code=${response?.code}")
                _connected.value = ConnectionStatus.DISCONNECTED
            }

            override fun onClosed(eventSource: EventSource) {
                Log.i(SSE_TAG, "closed segments=${segmentMap.size}")
                _connected.value = ConnectionStatus.DISCONNECTED
            }
        })
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
                _segments.value = segmentMap.values.sortedBy { it.timeline_start_sec }
            }

            // Log every segment arrival with full metadata
            segmentLogCount++
            val wordSample = segment.words.take(3).joinToString("|") { "${it.text}@${it.start_sec}-${it.end_sec}" }
            Log.i(SSE_TAG, "seg#${segmentLogCount} id=${segment.segment_id} " +
                "tl=[${segment.timeline_start_sec}-${segment.timeline_end_sec}] " +
                "text=${segment.text_zh.take(40)} " +
                "words=${segment.words.size} sample=[$wordSample]")
        } catch (e: Exception) {
            Log.w(SSE_TAG, "parse error: ${e.message}")
        }
    }

    override fun disconnect() {
        Log.i(SSE_TAG, "disconnect total_segments=${segmentMap.size}")
        eventSource?.cancel()
        eventSource = null
        _connected.value = ConnectionStatus.DISCONNECTED
        synchronized(lock) {
            segmentMap.clear()
            _segments.value = emptyList()
        }
    }
}

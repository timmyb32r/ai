package com.crimobile.service

import android.util.Log
import com.crimobile.api.SubtitleStreamSource
import com.crimobile.infrastructure.PCMRingBuffer
import com.crimobile.infrastructure.SubtitleParser
import com.crimobile.model.SubtitleEvent
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import okhttp3.OkHttpClient
import okhttp3.Request
import java.io.BufferedReader
import java.io.InputStreamReader
import java.util.Collections

/**
 * [SubtitleStreamSource] backed by OkHttp SSE streaming from the CRI
 * server's `/v1/stream/subtitles` endpoint.
 *
 * Accumulates subtitle events in a thread-safe synchronized list.
 * Periodically drained by the segment builder via [drainPending].
 */
class SseSubtitleSource(
    private val pcmBuffer: PCMRingBuffer,
    private val okHttp: OkHttpClient,
) : SubtitleStreamSource {

    @Volatile
    override var hasJumpEvent: Boolean = false
        private set

    private val _audioTimelineStartSec = MutableStateFlow<Double?>(null)
    override val audioTimelineStartSec: StateFlow<Double?> = _audioTimelineStartSec

    // Thread-safe: written from OkHttp callback, read from segment-builder thread.
    private val pending = Collections.synchronizedList(mutableListOf<SubtitleEvent>())

    private var call: okhttp3.Call? = null

    override fun connect(baseUrl: String) {
        disconnect()
        hasJumpEvent = false
        pending.clear()

        val request = Request.Builder()
            .url("$baseUrl/v1/stream/subtitles")
            .header("Accept", "text/event-stream")
            .build()

        call = okHttp.newCall(request)
        call!!.enqueue(object : okhttp3.Callback {
            override fun onFailure(call: okhttp3.Call, e: java.io.IOException) {
                if (!call.isCanceled()) Log.e(TAG, "SSE failed: ${e.message}")
            }

            override fun onResponse(call: okhttp3.Call, response: okhttp3.Response) {
                if (!response.isSuccessful) {
                    Log.e(TAG, "SSE HTTP ${response.code}")
                    return
                }
                Log.d(TAG, "SSE connected")
                val reader = BufferedReader(InputStreamReader(response.body?.byteStream()))
                var eventType = ""
                val data = StringBuilder()
                try {
                    while (true) {
                        val line = reader.readLine() ?: break
                        when {
                            line.isEmpty() -> {
                                val payload = data.toString().trim()
                                data.clear()
                                if (payload.isEmpty()) { eventType = ""; continue }
                                processEvent(eventType, payload)
                                eventType = ""
                            }
                            line.startsWith(":") -> {}
                            line.startsWith("event:") -> eventType = line.removePrefix("event:").trim()
                            line.startsWith("data:") -> {
                                if (data.isNotEmpty()) data.append('\n')
                                data.append(line.removePrefix("data:").trim())
                            }
                        }
                    }
                } catch (_: Exception) {}
                try { reader.close() } catch (_: Exception) {}
                Log.d(TAG, "SSE stream ended")
            }
        })
    }

    override fun drainPending(): List<SubtitleEvent> {
        synchronized(pending) {
            if (pending.isEmpty()) return emptyList()
            val copy = pending.toList()
            pending.clear()
            return copy
        }
    }

    override fun disconnect() {
        call?.cancel()
        call = null
        pending.clear()
    }

    private fun processEvent(type: String, payload: String) {
        try {
            when (type) {
                "sync" -> {
                    val ts = SubtitleParser.parseSync(payload)
                    if (ts != null) {
                        _audioTimelineStartSec.value = ts
                        Log.d(TAG, "SSE sync: audioTimelineStart=$ts")
                    } else Log.w(TAG, "Bad sync: $payload")
                }
                "jump" -> {
                    Log.d(TAG, "Server jump — reconnect needed")
                    hasJumpEvent = true
                }
                else -> {
                    val ev = SubtitleParser.parseSubtitle(payload)
                    if (ev != null) pending.add(ev)
                    else Log.w(TAG, "Bad subtitle: $payload")
                }
            }
        } catch (ex: Exception) {
            Log.w(TAG, "SSE parse error: $payload", ex)
        }
    }

    companion object {
        private const val TAG = "SseSubtitleSource"
    }
}

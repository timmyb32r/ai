package com.crimobile.offline

import com.crimobile.model.SubtitleSegment
import com.crimobile.subtitles.SubtitleParser
import kotlinx.coroutines.ensureActive
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.OkHttpClient
import okhttp3.Request
import org.json.JSONObject
import java.io.IOException
import java.time.Instant
import java.util.concurrent.TimeUnit

/** One download owns one bounded server snapshot. Legacy servers can omit the new fields. */
internal class ArchiveTransfer(
    private val serverUrl: String,
    private val client: OkHttpClient,
    private val nowMillis: () -> Long = System::currentTimeMillis
) {
    private var snapshotId: String? = null
    private var expiresAt: Long? = null

    fun checkLease() {
        if (expiresAt?.let { nowMillis() >= it } == true) throw IOException("Archive snapshot expired; restart the download")
    }

    suspend fun fetchSegments(start: Double, end: Double, onPage: suspend (Int, Int) -> Unit): List<SubtitleSegment> {
        val segments = mutableListOf<SubtitleSegment>()
        val ids = mutableSetOf<Int>()
        var expectedTotal: Int? = null
        var pageLimit = 500
        while (true) {
            kotlinx.coroutines.currentCoroutineContext().ensureActive()
            checkLease()
            val url = "$serverUrl/api/segments/range".toHttpUrl().newBuilder()
                .addQueryParameter("start_sec", start.toString()).addQueryParameter("end_sec", end.toString())
                .addQueryParameter("limit", pageLimit.toString()).addQueryParameter("offset", segments.size.toString())
            url.addQueryParameter("snapshot", snapshotId ?: "create")
            val request = Request.Builder().url(url.build()).build()
            val json = client.readCancellable(request) { response ->
                if (response.code == 413) return@readCancellable null
                if (!response.isSuccessful) throw IOException("Archive metadata HTTP ${response.code}")
                JSONObject(response.body?.string() ?: throw IOException("Empty metadata response"))
            }
            if (json == null) {
                if (pageLimit == 1) throw IOException("A single archive segment exceeds the server response limit")
                // Dictionary-rich pages can hit the server byte cap. Keep the
                // same offset and pin; a rejected initial snapshot is released
                // by the server, so its retry still uses snapshot=create.
                pageLimit = (pageLimit / 2).coerceAtLeast(1)
                continue
            }
            val token = json.optString("snapshot_id", "").takeIf { it.isNotEmpty() }
            if (expectedTotal == null && token != null) {
                snapshotId = token
                expiresAt = runCatching { Instant.parse(json.getString("snapshot_expires_at")).toEpochMilli() }
                    .getOrElse { throw IOException("Invalid snapshot expiry", it) }
            } else if (snapshotId != null && token != snapshotId) {
                throw IOException("Archive snapshot changed during pagination")
            }
            checkLease()
            val total = json.getInt("total")
            if (total !in 0..100_000 || expectedTotal?.let { it != total } == true) {
                throw IOException("Archive changed during pagination; restart the download")
            }
            expectedTotal = total
            val page = json.getJSONArray("segments")
            if (page.length() == 0 && segments.size < total) throw IOException("Incomplete archive metadata")
            for (i in 0 until page.length()) {
                val item = page.getJSONObject(i)
                val segment = SubtitleParser.parseSegment(item.optJSONObject("segment") ?: item)
                if (!ids.add(segment.segment_id)) throw IOException("Duplicate archive segment ${segment.segment_id}")
                if (!segment.ts_file.matches(Regex("\\d+\\.ts")) ||
                    !segment.timeline_start_sec.isFinite() || !segment.timeline_end_sec.isFinite() ||
                    segment.timeline_end_sec <= segment.timeline_start_sec) throw IOException("Invalid archive segment")
                segments.add(segment)
            }
            if (segments.size > total) throw IOException("Unexpected archive segment count")
            onPage(segments.size, total)
            if (segments.size == total) return segments
        }
    }

    suspend fun audio(segment: SubtitleSegment): ByteArray {
        checkLease()
        val url = "$serverUrl/hls/${segment.ts_file}".toHttpUrl().newBuilder()
        snapshotId?.let { url.addQueryParameter("snapshot", it) }
        return client.readCancellable(Request.Builder().url(url.build()).build()) { response ->
            if (!response.isSuccessful) throw IOException("Audio ${segment.ts_file}: HTTP ${response.code}")
            val bytes = response.body?.bytes() ?: throw IOException("Empty audio ${segment.ts_file}")
            if (bytes.isEmpty()) throw IOException("Empty audio ${segment.ts_file}")
            checkLease()
            bytes
        }
    }

    fun release() {
        val id = snapshotId ?: return
        snapshotId = null
        val url = "$serverUrl/api/snapshots".toHttpUrl().newBuilder().addPathSegment(id).build()
        runCatching {
            client.newBuilder().callTimeout(2, TimeUnit.SECONDS).build()
                .newCall(Request.Builder().url(url).delete().build()).execute().use { }
        }
    }
}

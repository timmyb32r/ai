package com.crimobile.player

import java.time.OffsetDateTime

/** Media time is continuous across HLS discontinuities; wall-clock time need not be. */
class HlsTimelineMap(val segments: List<Segment>) {
    data class Segment(val file: String, val positionMs: Long, val epochMs: Long, val durationMs: Long)
    data class Position(val segment: Segment, val epochMs: Long)

    fun at(positionMs: Long): Position? {
        val index = segments.binarySearchBy(positionMs) { it.positionMs }
        val i = if (index >= 0) index else -index - 2
        val s = segments.getOrNull(i) ?: return null
        if (positionMs >= s.positionMs + s.durationMs) return null
        return Position(s, s.epochMs + positionMs - s.positionMs)
    }

    /** Seeking into an outage lands at the first available audio after it. */
    fun positionFor(epochMs: Long): Long? {
        for (s in segments) {
            if (epochMs < s.epochMs) return s.positionMs
            if (epochMs < s.epochMs + s.durationMs) return s.positionMs + epochMs - s.epochMs
        }
        return segments.lastOrNull()?.let { it.positionMs + it.durationMs - 1 }
    }

    fun gapBefore(file: String): Long {
        val i = segments.indexOfFirst { it.file == file }
        if (i < 1) return 0
        return (segments[i].epochMs - segments[i - 1].epochMs - segments[i - 1].durationMs).coerceAtLeast(0)
    }

    companion object {
        fun parse(text: String): HlsTimelineMap? {
            val segments = mutableListOf<Segment>()
            var positionUs = 0L
            var epochUs: Long? = null
            var durationUs: Long? = null
            for (raw in text.lineSequence()) {
                val line = raw.trim()
                when {
                    line.startsWith("#EXT-X-PROGRAM-DATE-TIME:") -> {
                        epochUs = runCatching {
                            val instant = OffsetDateTime.parse(line.substringAfter(':')).toInstant()
                            Math.addExact(Math.multiplyExact(instant.epochSecond, 1_000_000L), instant.nano / 1000L)
                        }.getOrNull()
                            ?: return null
                    }
                    line.startsWith("#EXTINF:") -> {
                        val seconds = line.substringAfter(':').substringBefore(',').toDoubleOrNull() ?: return null
                        if (!seconds.isFinite() || seconds <= 0 || seconds > 3600) return null
                        durationUs = (seconds * 1_000_000).toLong().coerceAtLeast(1000)
                    }
                    line.isNotEmpty() && !line.startsWith('#') -> {
                        val d = durationUs ?: return null // multivariant playlists are handled by Media3
                        val e = epochUs ?: return null
                        if (segments.lastOrNull()?.let { e / 1000 < it.epochMs + it.durationMs - 2 } == true) return null
                        segments.add(Segment(line, positionUs / 1000, e / 1000, (positionUs + d) / 1000 - positionUs / 1000))
                        positionUs += d
                        epochUs = e + d
                        durationUs = null
                    }
                }
            }
            return segments.takeIf { it.isNotEmpty() }?.let(::HlsTimelineMap)
        }
    }
}

/** Notification follows the playback cursor, never a newly downloaded playlist. */
class PlaybackGapTracker {
    private var previous: HlsTimelineMap.Position? = null
    private val notified = linkedSetOf<String>()
    fun reset() { previous = null; notified.clear() }
    fun resetPosition() { previous = null }
    fun advance(map: HlsTimelineMap, positionMs: Long): Long {
        val current = map.at(positionMs) ?: return 0
        val before = previous
        previous = current
        if (before == null || before.segment.file == current.segment.file || current.epochMs < before.epochMs) return 0
        val gap = map.gapBefore(current.segment.file)
        if (gap < 250 || !notified.add(current.segment.file)) return 0
        while (notified.size > 128) notified.remove(notified.first())
        return gap
    }
}

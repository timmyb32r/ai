package com.crimobile.player

import org.junit.Assert.*
import org.junit.Test
import java.time.Instant

class HlsTimelineMapTest {
    private val start = Instant.parse("2026-09-18T10:00:00Z").toEpochMilli()
    private fun playlist() = HlsTimelineMap.parse("""
        #EXTM3U
        #EXT-X-PROGRAM-DATE-TIME:2026-09-18T10:00:00Z
        #EXTINF:3.040,
        000000001.ts
        #EXT-X-PROGRAM-DATE-TIME:2026-09-18T10:00:03.040Z
        #EXTINF:2.960,
        000000002.ts
        #EXT-X-DISCONTINUITY
        #EXT-X-PROGRAM-DATE-TIME:2026-09-18T10:05:00Z
        #EXTINF:3,
        000000010.ts
    """.trimIndent())!!

    @Test fun `maps exact durations and clock gap rather than adding media position to first epoch`() {
        val map = playlist()
        assertEquals(start + 3500, map.at(3500)!!.epochMs)
        assertEquals(start + 300_000, map.at(6000)!!.epochMs)
        assertEquals(start + 301_000, map.at(7000)!!.epochMs)
        assertEquals(7000L, map.positionFor(start + 301_000))
        assertEquals(6000L, map.positionFor(start + 120_000))
    }

    @Test fun `learning about future gap does not skip playback or notify until cursor crosses`() {
        val map = playlist()
        val tracker = PlaybackGapTracker()
        assertEquals(0L, tracker.advance(map, 1000))
        assertEquals(0L, tracker.advance(map, 4000))
        assertEquals(294_000L, tracker.advance(map, 6001))
        assertEquals(0L, tracker.advance(map, 6500))
        tracker.resetPosition() // explicit user seek must not report an outage skipped by autoplay
        assertEquals(0L, tracker.advance(map, 6100))
    }

    @Test fun `legacy playlist with only first PDT remains linear`() {
        val map = HlsTimelineMap.parse("#EXTM3U\n#EXT-X-PROGRAM-DATE-TIME:2026-09-18T10:00:00Z\n#EXTINF:3,\n1.ts\n#EXTINF:3,\n2.ts")!!
        assertEquals(start + 4500, map.at(4500)!!.epochMs)
        assertEquals(0L, map.gapBefore("2.ts"))
    }

    @Test fun `sliding playlist still maps current media coordinates`() {
        val first = playlist()
        val slid = HlsTimelineMap(first.segments.drop(1).map { it.copy(positionMs = it.positionMs - 3040) })
        assertEquals(first.at(7000)!!.epochMs, slid.at(3960)!!.epochMs)
    }

    @Test fun `submillisecond durations accumulate before conversion to player milliseconds`() {
        val text = "#EXTM3U\n#EXT-X-PROGRAM-DATE-TIME:2026-09-18T10:00:00Z\n" +
            (1..1000).joinToString("\n") { "#EXTINF:3.0005,\n$it.ts" }
        val map = HlsTimelineMap.parse(text)!!
        assertEquals(3_000_500L, map.segments.last().positionMs + map.segments.last().durationMs)
        assertEquals(start + 2_999_000L, map.at(2_999_000)!!.epochMs)
    }

    @Test fun `invalid durations and absent clock reject map instead of corrupting epoch`() {
        assertNull(HlsTimelineMap.parse("#EXTINF:3,\n1.ts"))
        assertNull(HlsTimelineMap.parse("#EXT-X-PROGRAM-DATE-TIME:2026-09-18T10:00:00Z\n#EXTINF:NaN,\n1.ts"))
    }
}

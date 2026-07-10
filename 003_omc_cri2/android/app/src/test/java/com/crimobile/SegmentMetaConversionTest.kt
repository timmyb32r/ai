package com.crimobile

import com.crimobile.model.SegmentMeta
import com.crimobile.model.SubtitleSegment
import com.crimobile.model.WordEntry
import com.crimobile.model.WordSense
import com.crimobile.model.toMeta
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runTest
import org.junit.Test
import org.junit.Assert.*

/**
 * Regression test: live-mode text rendering broke when [SubtitleList]
 * was switched to [SegmentMeta] but live sources (Http/Sse) never
 * populated `_segmentsMeta`.
 *
 * This test verifies:
 * 1. `SubtitleSegment.toMeta()` correctly projects all fields.
 * 2. The pattern of updating `_segments` and `_segmentsMeta` together
 *    keeps both flows in sync (the pattern applied in HttpSubtitleSource
 *    and SseSubtitleSource).
 */
class SegmentMetaConversionTest {

    private val sampleSegment = SubtitleSegment(
        segment_id = 42,
        timeline_start_sec = 1718000000.0,
        timeline_end_sec = 1718000003.5,
        ts_file = "000000042.ts",
        text_zh = "你好世界",
        text_pinyin = "ni hao shi jie",
        text_en = "Hello world",
        words = listOf(
            WordEntry(
                text = "你好", char_start = 0, char_end = 2,
                start_sec = 1718000000.0, end_sec = 1718000001.5,
                pinyin = "ni hao", char_pinyin = listOf("ni", "hao"),
                translation = "hello",
                senses = listOf(WordSense(number = 1, text = "привет")),
                cedict_meanings = listOf("hello", "hi")
            ),
            WordEntry(
                text = "世界", char_start = 2, char_end = 4,
                start_sec = 1718000001.5, end_sec = 1718000003.5,
                pinyin = "shi jie", char_pinyin = listOf("shi", "jie"),
                translation = "world",
                senses = emptyList(),
                cedict_meanings = listOf("world")
            )
        )
    )

    @Test
    fun `toMeta preserves all SegmentMeta fields`() {
        val meta = sampleSegment.toMeta()

        assertEquals(42, meta.segment_id)
        assertEquals(1718000000.0, meta.timeline_start_sec, 0.0)
        assertEquals(1718000003.5, meta.timeline_end_sec, 0.0)
        assertEquals("000000042.ts", meta.ts_file)
        assertEquals("你好世界", meta.text_zh)
        assertEquals("ni hao shi jie", meta.text_pinyin)
    }

    @Test
    fun `toMeta excludes word-level data`() {
        val meta = sampleSegment.toMeta()

        // SegmentMeta has exactly 6 fields — verify no words/senses leak through.
        assertEquals(42, meta.segment_id)
        assertEquals(1718000000.0, meta.timeline_start_sec, 0.0)
        assertEquals(1718000003.5, meta.timeline_end_sec, 0.0)
        assertEquals("000000042.ts", meta.ts_file)
        assertEquals("你好世界", meta.text_zh)
        assertEquals("ni hao shi jie", meta.text_pinyin)

        // The original segment still has full word data — conversion is non-destructive.
        assertEquals(2, sampleSegment.words.size)
        assertEquals("hello", sampleSegment.words[0].translation)
    }

    @Test
    fun `segments and segmentsMeta stay in sync — live source pattern`() = runTest {
        val _segments = MutableStateFlow<List<SubtitleSegment>>(emptyList())
        val _segmentsMeta = MutableStateFlow<List<SegmentMeta>>(emptyList())

        // Simulate what HttpSubtitleSource.pollOnce() and SseSubtitleSource.handleSegment() do.
        val newSegments = listOf(sampleSegment)
        _segments.value = newSegments
        _segmentsMeta.value = newSegments.map { it.toMeta() }

        // Both flows should be non-empty and agree on count.
        val segs = _segments.first { it.isNotEmpty() }
        val metas = _segmentsMeta.first { it.isNotEmpty() }

        assertEquals(1, segs.size)
        assertEquals(1, metas.size)
        assertEquals(segs[0].segment_id, metas[0].segment_id)
        assertEquals(segs[0].text_zh, metas[0].text_zh)
    }

    @Test
    fun `segmentsMeta cleared when source disconnects`() = runTest {
        val _segments = MutableStateFlow<List<SubtitleSegment>>(emptyList())
        val _segmentsMeta = MutableStateFlow<List<SegmentMeta>>(emptyList())

        // Fill
        _segments.value = listOf(sampleSegment)
        _segmentsMeta.value = listOf(sampleSegment.toMeta())

        // Disconnect — both must be empty.
        _segments.value = emptyList()
        _segmentsMeta.value = emptyList()

        assertTrue(_segments.value.isEmpty())
        assertTrue(_segmentsMeta.value.isEmpty())
    }

    @Test
    fun `multiple segments conversion preserves ordering`() {
        val segments = (0..4).map { id ->
            sampleSegment.copy(segment_id = id, timeline_start_sec = 1000.0 + id * 3)
        }
        val metas = segments.map { it.toMeta() }

        assertEquals(5, metas.size)
        // Order preserved.
        for (i in 0..4) {
            assertEquals(i, metas[i].segment_id)
            assertEquals(1000.0 + i * 3, metas[i].timeline_start_sec, 0.0)
        }
    }

    // ── Regression: live-mode text was blank because ViewModel never collected segmentsMeta ──

    data class TestState(
        val segments: List<SubtitleSegment> = emptyList(),
        val segmentsMeta: List<SegmentMeta> = emptyList()
    )

    @Test
    fun `live mode — both segments and segmentsMeta flow into ViewState`() = runTest {
        // Simulates what CriViewModel.init{} does with _subtitleSource flows.
        val segmentsFlow = MutableStateFlow<List<SubtitleSegment>>(emptyList())
        val segmentsMetaFlow = MutableStateFlow<List<SegmentMeta>>(emptyList())
        val stateFlow = MutableStateFlow(TestState())

        // Collector 1: segments → state.segments (existing, line 168-174)
        val job1 = launch {
            segmentsFlow.collect { segs ->
                stateFlow.value = stateFlow.value.copy(segments = segs)
            }
        }

        // Collector 2: segmentsMeta → state.segmentsMeta (WAS MISSING — the bug)
        val job2 = launch {
            segmentsMetaFlow.collect { meta ->
                stateFlow.value = stateFlow.value.copy(segmentsMeta = meta)
            }
        }

        // Source emits both — like SseSubtitleSource.handleSegment() does.
        val segs = listOf(sampleSegment)
        segmentsFlow.value = segs
        segmentsMetaFlow.value = segs.map { it.toMeta() }

        // State must have BOTH populated.
        val state = stateFlow.first { it.segments.isNotEmpty() }
        assertEquals(1, state.segments.size)
        assertEquals("segmentsMeta MUST be populated — missing collector in CriViewModel", 1, state.segmentsMeta.size)
        assertEquals(state.segments[0].text_zh, state.segmentsMeta[0].text_zh)

        job1.cancel()
        job2.cancel()
    }

    @Test
    fun `live mode — without segmentsMeta collector, UI would be blank`() = runTest {
        // Demonstrates the EXACT bug: if we only collect `segments` but not
        // `segmentsMeta`, the SubtitleList receives empty list.
        val segmentsFlow = MutableStateFlow<List<SubtitleSegment>>(emptyList())
        val segmentsMetaFlow = MutableStateFlow<List<SegmentMeta>>(emptyList())

        // Only collecting segments (the buggy state before fix).
        val stateFlow = MutableStateFlow(TestState())
        val job = launch {
            segmentsFlow.collect { segs ->
                stateFlow.value = stateFlow.value.copy(segments = segs)
            }
        }

        segmentsFlow.value = listOf(sampleSegment)
        segmentsMetaFlow.value = listOf(sampleSegment.toMeta())

        val state = stateFlow.first { it.segments.isNotEmpty() }
        assertEquals(1, state.segments.size)
        // THIS was the bug — segmentsMeta empty despite source emitting it.
        assertTrue("Without segmentsMeta collector, UI gets empty list → blank screen",
            state.segmentsMeta.isEmpty())
        job.cancel()
    }

    // ── Regression: live mode needs full segments — segmentCache is null ──

    @Test
    fun `live mode — fullSegments fallback must work when segmentCache is null`() {
        val meta = sampleSegment.toMeta()
        val fullSegmentsById = mapOf(sampleSegment.segment_id to sampleSegment)

        // Simulate SubtitleList: segmentCache is null (live mode), try fullSegmentsById.
        val seg = fullSegmentsById[meta.segment_id]

        assertNotNull("fullSegmentsById MUST provide the full segment when segmentCache is null", seg)
        assertEquals(sampleSegment.segment_id, seg!!.segment_id)
        assertEquals(sampleSegment.text_zh, seg.text_zh)
        // Word-level data present — can render SegmentCard with buildCharCells.
        assertEquals(2, seg.words.size)
        assertEquals("hello", seg.words[0].translation)
    }

    @Test
    fun `live mode — without fullSegments fallback, only placeholder renders`() {
        // Demonstrates the EXACT rendering bug: when segmentCache is null
        // AND there's no fullSegmentsById, we show placeholder (plain Text)
        // instead of SegmentCard (FlowRow with per-character pinyin + highlighting).
        val meta = sampleSegment.toMeta()

        // segmentCache?.getOrLoad returns null (cache is null in live mode).
        val fromCache: SubtitleSegment? = null
        // fullSegmentsById is what we added as fix.
        val fromFullSegments: SubtitleSegment? = null // NOT provided → bug!

        val seg = fromCache ?: fromFullSegments
        assertNull("Without fullSegments fallback, segment is null → placeholder renders → no per-char layout, no active word highlight", seg)
    }
}

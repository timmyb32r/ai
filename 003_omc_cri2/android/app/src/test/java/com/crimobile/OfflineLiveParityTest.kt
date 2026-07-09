package com.crimobile

import com.crimobile.subtitles.SubtitleParser
import com.crimobile.ui.buildCharCells
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

/**
 * Guards that OFFLINE data can never drift from LIVE data.
 *
 * Historically the offline path used a second, hand-written JSON parser
 * (DownloadEngine) and a separate cache serializer (OfflineStorageManager) that
 * silently dropped `char_pinyin`. The result: offline segments had empty
 * per-character pinyin, so the UI fell back to painting the WHOLE-WORD pinyin
 * over the first character — exactly the reported bug.
 *
 * The fix funnels every subtitle source through ONE parser
 * ([SubtitleParser.parseSegment]) and ONE serializer
 * ([SubtitleParser.segmentToJson]). These tests lock that in: a fully-populated
 * segment must survive the offline round-trip byte-for-field identical to the
 * live parse, and the whole-word-pinyin-on-first-char symptom must not recur.
 */
class OfflineLiveParityTest {

    // A segment with EVERY word-level field populated with a distinctive value.
    // If any field is dropped by the serializer, the round-trip equality below
    // fails — which is what forces new fields to be handled in both directions.
    private val richServerJson = """
    {
      "segment_id": 7,
      "timeline_start_sec": 100.5,
      "timeline_end_sec": 103.5,
      "ts_file": "000000007.ts",
      "text_zh": "呵护的",
      "text_pinyin": "hē hù de",
      "text_en": "to cherish / possessive particle",
      "words": [
        {
          "text": "呵护", "char_start": 0, "char_end": 2,
          "start_sec": 100.5, "end_sec": 102.0,
          "pinyin": "hē hù",
          "char_pinyin": ["hē", "hù"],
          "char_pinyin_uncertain": [false, false],
          "translation": "оберегать",
          "senses": [{"number": 1, "labels": ["перен."], "text": "оберегать", "notes": "забота"}],
          "cedict_meanings": ["to cherish", "to protect"]
        },
        {
          "text": "的", "char_start": 2, "char_end": 3,
          "start_sec": 102.0, "end_sec": 103.5,
          "pinyin": "de",
          "char_pinyin": ["de"],
          "char_pinyin_uncertain": [true],
          "translation": "частица",
          "cedict_meanings": ["possessive particle"]
        }
      ]
    }
    """.trimIndent()

    /** Simulates the full offline journey: parse (download) → cache → reload. */
    private fun offlineRoundTrip(segJson: JSONObject) =
        SubtitleParser.parseSegment(
            SubtitleParser.segmentToJson(SubtitleParser.parseSegment(segJson))
        )

    @Test
    fun `offline round-trip is identical to live parse`() {
        val segJson = JSONObject(richServerJson)
        val live = SubtitleParser.parseSegment(segJson)
        val offline = offlineRoundTrip(segJson)

        // Data-class structural equality covers EVERY field of the segment and
        // its words — the single assertion that catches any dropped field.
        assertEquals("offline segment must equal live segment", live, offline)
    }

    @Test
    fun `drift-prone fields survive the offline round-trip`() {
        val offline = offlineRoundTrip(JSONObject(richServerJson))
        val hehu = offline.words[0]
        val de = offline.words[1]

        assertEquals(listOf("hē", "hù"), hehu.char_pinyin)
        assertEquals(listOf(false, false), hehu.char_pinyin_uncertain)
        assertEquals(listOf("to cherish", "to protect"), hehu.cedict_meanings)
        assertEquals(1, hehu.senses.size)
        assertEquals("оберегать", hehu.senses[0].text)
        assertEquals(listOf("перен."), hehu.senses[0].labels)

        assertEquals(listOf("de"), de.char_pinyin)
        assertEquals(listOf(true), de.char_pinyin_uncertain)
        assertEquals(listOf("possessive particle"), de.cedict_meanings)
    }

    @Test
    fun `offline never paints whole-word pinyin over the first character`() {
        val offline = offlineRoundTrip(JSONObject(richServerJson))
        val cells = buildCharCells(offline.words, showPinyin = true)

        val he = cells.first { it.text == "呵" }
        val hu = cells.first { it.text == "护" }

        // Each character carries its OWN syllable, not the whole word.
        assertEquals("hē", he.syllable)
        assertEquals("hù", hu.syllable)

        // The whole-word pinyin must never appear on any cell.
        assertTrue(
            "whole-word pinyin leaked onto a character",
            cells.none { it.syllable == "hēhù" || it.syllable == "hē hù" || it.syllable == "hehu" }
        )
    }

    @Test
    fun `buildCharCells output is identical for live and offline`() {
        val segJson = JSONObject(richServerJson)
        val liveCells = buildCharCells(SubtitleParser.parseSegment(segJson).words, showPinyin = true)
        val offlineCells = buildCharCells(offlineRoundTrip(segJson).words, showPinyin = true)

        assertEquals(liveCells.size, offlineCells.size)
        for (i in liveCells.indices) {
            assertEquals("cell $i text", liveCells[i].text, offlineCells[i].text)
            assertEquals("cell $i syllable", liveCells[i].syllable, offlineCells[i].syllable)
            assertEquals("cell $i uncertain", liveCells[i].uncertain, offlineCells[i].uncertain)
        }
    }
}

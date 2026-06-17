package com.crimobile

import com.crimobile.model.SubtitleSegment
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test

/**
 * Tests that JSON from the server is correctly parsed into SubtitleSegment.
 * Uses real server output to guarantee parsing doesn't silently break.
 */
class SseSubtitleParsingTest {

    // Real SSE data from server (captured 2026-06-17)
    private val realServerJson = """
    {"type":"segment","segment":{"segment_id":3,"timeline_start_sec":1781645187.332,"timeline_end_sec":1781645190.332,"ts_file":"000000003.ts","text_zh":"有大暴雨光西東南部也要海等","text_pinyin":"you3  guang1  nan2 bu4 ye3 yao4 hai3 deng3","text_en":"to have; there is  light; ray (CL:道[dao4])  southern part also; too; as well; (not ...) either to want; to need; to ask for ocean to wait for; to await","words":[{"text":"有","char_start":0,"char_end":1,"start_sec":1781645187.332,"end_sec":1781645187.5627692,"pinyin":"you3","translation":"to have; there is"},{"text":"大暴雨","char_start":1,"char_end":4,"start_sec":1781645187.5627692,"end_sec":1781645188.255077,"pinyin":"","translation":""},{"text":"光","char_start":4,"char_end":5,"start_sec":1781645188.255077,"end_sec":1781645188.485846,"pinyin":"guang1","translation":"light; ray (CL:道[dao4])"},{"text":"西東","char_start":5,"char_end":7,"start_sec":1781645188.485846,"end_sec":1781645188.9473846,"pinyin":"","translation":""},{"text":"南部","char_start":7,"char_end":9,"start_sec":1781645188.9473846,"end_sec":1781645189.4089231,"pinyin":"nan2 bu4","translation":"southern part"},{"text":"也","char_start":9,"char_end":10,"start_sec":1781645189.4089231,"end_sec":1781645189.6396923,"pinyin":"ye3","translation":"also; too; as well; (not ...) either"},{"text":"要","char_start":10,"char_end":11,"start_sec":1781645189.6396923,"end_sec":1781645189.8704615,"pinyin":"yao4","translation":"to want; to need; to ask for"},{"text":"海","char_start":11,"char_end":12,"start_sec":1781645189.8704615,"end_sec":1781645190.1012306,"pinyin":"hai3","translation":"ocean"},{"text":"等","char_start":12,"char_end":13,"start_sec":1781645190.1012306,"end_sec":1781645190.3319998,"pinyin":"deng3","translation":"to wait for; to await"}]}}
    """.trimIndent()

    @Test
    fun `parse real server JSON — segment fields`() {
        val json = JSONObject(realServerJson)
        val segJson = json.getJSONObject("segment")
        val seg = parseFromJson(segJson)

        assertEquals(3, seg.segment_id)
        assertEquals(1_781_645_187.332, seg.timeline_start_sec, 0.001)
        assertEquals(1_781_645_190.332, seg.timeline_end_sec, 0.001)
        assertEquals("000000003.ts", seg.ts_file)
        assertEquals("有大暴雨光西東南部也要海等", seg.text_zh)
    }

    @Test
    fun `parse real server JSON — word count`() {
        val json = JSONObject(realServerJson)
        val segJson = json.getJSONObject("segment")
        val seg = parseFromJson(segJson)

        assertEquals("should have 9 words", 9, seg.words.size)
    }

    @Test
    fun `parse real server JSON — first word`() {
        val json = JSONObject(realServerJson)
        val segJson = json.getJSONObject("segment")
        val seg = parseFromJson(segJson)

        val first = seg.words.first()
        assertEquals("有", first.text)
        assertEquals(0, first.char_start)
        assertEquals(1, first.char_end)
        assertEquals(1_781_645_187.332, first.start_sec, 0.001)
        assertEquals("you3", first.pinyin)
        assertEquals("to have; there is", first.translation)
    }

    @Test
    fun `parse real server JSON — last word`() {
        val json = JSONObject(realServerJson)
        val segJson = json.getJSONObject("segment")
        val seg = parseFromJson(segJson)

        val last = seg.words.last()
        assertEquals("等", last.text)
        assertEquals(12, last.char_start)
        assertEquals(13, last.char_end)
        assertEquals(1_781_645_190.1012306, last.start_sec, 0.001)
        assertEquals(1_781_645_190.3319998, last.end_sec, 0.001)
    }

    @Test
    fun `parse real server JSON — word timestamps are within segment`() {
        val json = JSONObject(realServerJson)
        val segJson = json.getJSONObject("segment")
        val seg = parseFromJson(segJson)

        for (word in seg.words) {
            assertTrue(
                "word '${word.text}': start_sec ${word.start_sec} must be >= segment start ${seg.timeline_start_sec}",
                word.start_sec >= seg.timeline_start_sec
            )
            assertTrue(
                "word '${word.text}': end_sec ${word.end_sec} must be <= segment end ${seg.timeline_end_sec}",
                word.end_sec <= seg.timeline_end_sec
            )
        }
    }

    @Test
    fun `parse real server JSON — round-trip text reconstruction`() {
        val json = JSONObject(realServerJson)
        val segJson = json.getJSONObject("segment")
        val seg = parseFromJson(segJson)

        val reconstructed = seg.words.joinToString("") { it.text }
        assertEquals(seg.text_zh, reconstructed)
    }

    // ── Edge cases ────────────────────────────────────────────────────

    @Test
    fun `parse segment with no words`() {
        val json = JSONObject("""
            {"segment_id":0,"timeline_start_sec":0.0,"timeline_end_sec":3.0,
             "ts_file":"x.ts","text_zh":"","text_pinyin":"","text_en":"","words":[]}
        """.trimIndent())
        val seg = parseFromJson(json)
        assertEquals(0, seg.words.size)
        assertEquals("", seg.text_zh)
    }

    @Test
    fun `parse segment with single word`() {
        val json = JSONObject("""
            {"segment_id":1,"timeline_start_sec":3.0,"timeline_end_sec":6.0,
             "ts_file":"x.ts","text_zh":"测试","text_pinyin":"","text_en":"",
             "words":[{"text":"测试","char_start":0,"char_end":2,
                       "start_sec":3.0,"end_sec":6.0,"pinyin":"cèshì","translation":"test"}]}
        """.trimIndent())
        val seg = parseFromJson(json)
        assertEquals(1, seg.words.size)
        assertEquals("测试", seg.words[0].text)
        assertEquals(3.0, seg.words[0].start_sec, 0.001)
        assertEquals(6.0, seg.words[0].end_sec, 0.001)
    }

    // ── Helper: matches SseSubtitleSource.parseSegment ────────────────

    private fun parseFromJson(json: JSONObject): SubtitleSegment {
        val wordsArray = json.optJSONArray("words") ?: org.json.JSONArray()
        val words = mutableListOf<com.crimobile.model.WordEntry>()
        for (i in 0 until wordsArray.length()) {
            val w = wordsArray.getJSONObject(i)
            words.add(
                com.crimobile.model.WordEntry(
                    text = w.optString("text", ""),
                    char_start = w.optInt("char_start", 0),
                    char_end = w.optInt("char_end", 0),
                    start_sec = w.optDouble("start_sec", 0.0),
                    end_sec = w.optDouble("end_sec", 0.0),
                    pinyin = w.optString("pinyin", ""),
                    translation = w.optString("translation", "")
                )
            )
        }
        return SubtitleSegment(
            segment_id = json.optInt("segment_id", 0),
            timeline_start_sec = json.optDouble("timeline_start_sec", 0.0),
            timeline_end_sec = json.optDouble("timeline_end_sec", 0.0),
            ts_file = json.optString("ts_file", ""),
            text_zh = json.optString("text_zh", ""),
            text_pinyin = json.optString("text_pinyin", ""),
            text_en = json.optString("text_en", ""),
            words = words
        )
    }
}

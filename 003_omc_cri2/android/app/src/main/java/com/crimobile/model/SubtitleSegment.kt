package com.crimobile.model

data class SubtitleSegment(
    val segment_id: Int,
    val timeline_start_sec: Double,
    val timeline_end_sec: Double,
    val ts_file: String,
    val text_zh: String,
    val text_pinyin: String,
    val text_en: String,
    val words: List<WordEntry> = emptyList()
)

/**
 * Lightweight segment metadata — always kept in RAM.
 * Contains only the fields needed for timeline navigation,
 * UI rendering (text+pinyin), and audio playback.
 *
 * ~200-500 bytes per segment vs 5-20 KB for full [SubtitleSegment].
 */
data class SegmentMeta(
    val segment_id: Int,
    val timeline_start_sec: Double,
    val timeline_end_sec: Double,
    val ts_file: String,
    val text_zh: String,
    val text_pinyin: String
)

/** Lightweight conversion: drops word-level data (senses, pinyin arrays, etc.). */
fun SubtitleSegment.toMeta() = SegmentMeta(
    segment_id = segment_id,
    timeline_start_sec = timeline_start_sec,
    timeline_end_sec = timeline_end_sec,
    ts_file = ts_file,
    text_zh = text_zh,
    text_pinyin = text_pinyin
)


data class WordSense(
    val number: Int = 0,
    val labels: List<String> = emptyList(),
    val text: String = "",
    val notes: String = ""
)

data class WordEntry(
    val text: String,
    val char_start: Int,
    val char_end: Int,
    val start_sec: Double,
    val end_sec: Double,
    val pinyin: String,
    val char_pinyin: List<String> = emptyList(),
    // Aligned with char_pinyin: true = reading was filled probabilistically
    // (Unihan frequency), not derived deterministically. Absent → all false.
    val char_pinyin_uncertain: List<Boolean> = emptyList(),
    val translation: String,
    val senses: List<WordSense> = emptyList(),
    // CC-CEDICT English glosses (second dictionary), if the word is in CEDICT.
    val cedict_meanings: List<String> = emptyList(),
    // Wiktionary English glosses (third dictionary), if the word is in Wiktionary.
    val wiktionary_meanings: List<String> = emptyList()
)


data class SseSync(
    val type: String,
    val timeline_start_sec: Double,
    val server_time: String
)


data class SseSegment(
    val type: String,
    val segment: SubtitleSegment
)

enum class PlaybackState {
    IDLE, LOADING, PLAYING, PAUSED, ERROR
}

enum class ConnectionStatus {
    DISCONNECTED, CONNECTING, CONNECTED
}

data class WordPopupState(
    val word: WordEntry,
    val segment: SubtitleSegment,
    val pinyin: String,
    val translation: String,
    val senses: List<WordSense> = emptyList(),
    val cedictMeanings: List<String> = emptyList(),
    val wiktionaryMeanings: List<String> = emptyList()
)

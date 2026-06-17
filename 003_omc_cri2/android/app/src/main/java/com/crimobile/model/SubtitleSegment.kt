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


data class WordEntry(
    val text: String,
    val char_start: Int,
    val char_end: Int,
    val start_sec: Double,
    val end_sec: Double,
    val pinyin: String,
    val translation: String
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
    val translation: String
)

package com.crimobile.model

/**
 * One timestamped subtitle line from the ASR pipeline.
 *
 * @property start Absolute broadcast-timeline start (seconds).
 * @property end   Absolute broadcast-timeline end (seconds).
 * @property textZh Simplified-Chinese transcript for [start, end].
 * @property words Per-word character offsets and timestamps (null if unavailable).
 * @property pinyin Pinyin romanization of the full subtitle (may be empty).
 * @property en    English translation of the full subtitle (may be empty).
 */
data class SubtitleEvent(
    val start: Double,
    val end: Double,
    val textZh: String,
    val words: List<WordBoundary>? = null,
    val pinyin: String = "",
    val en: String = "",
)

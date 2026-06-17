package com.crimobile.model

/**
 * One word within a [SubtitleEvent], identified by rune (character) offsets
 * into [SubtitleEvent.textZh].
 *
 * @property charStart 0-based index of the first character of the word.
 * @property charEnd   Exclusive end (one past the last character).
 * @property startSec  Per-word start time relative to subtitle start (0 if unavailable).
 * @property endSec    Per-word end time relative to subtitle start (0 if unavailable).
 * @property pinyin    Pinyin romanization of this word (may be empty).
 * @property en        English translation of this word (may be empty).
 */
data class WordBoundary(
    val charStart: Int,
    val charEnd: Int,
    val startSec: Double = 0.0,
    val endSec: Double = 0.0,
    val pinyin: String = "",
    val en: String = "",
) {
    init {
        require(charStart >= 0) { "charStart must be >= 0, got $charStart" }
        require(charEnd > charStart) {
            "charEnd ($charEnd) must be > charStart ($charStart)"
        }
    }

    /**
     * Extracts the word's text from a full subtitle string.
     * Safe — coerces indices into valid bounds.
     */
    fun substring(fullText: String): String {
        val s = charStart.coerceIn(0, fullText.length)
        val e = charEnd.coerceIn(0, fullText.length)
        return fullText.substring(s, e)
    }
}

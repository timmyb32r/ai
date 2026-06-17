package com.crimobile.model

/**
 * A display-ready subtitle segment with optional audio.
 *
 * In the current flat-subtitle-list architecture [pcm] may be empty
 * ([ByteArray(0)]) — audio is streamed separately through
 * [com.crimobile.infrastructure.PCMRingBuffer].  The segment exists to carry
 * subtitle text, word boundaries, pinyin, and translation to the UI.
 *
 * ## Invariants
 * - [startSec] < [endSec]
 * - [subtitle.textZh] is non-empty
 */
data class SyncedSegment(
    /** Absolute broadcast-timeline start (seconds). */
    val startSec: Double,
    /** Absolute broadcast-timeline end (seconds). */
    val endSec: Double,
    /** Raw s16le PCM; may be empty in flat-subtitle-list mode. */
    val pcm: ByteArray,
    /** Subtitle text, pinyin, translation, and word boundaries. */
    val subtitle: SubtitleEvent,
) {
    init {
        require(startSec < endSec) {
            "startSec ($startSec) must be < endSec ($endSec)"
        }
        require(subtitle.textZh.isNotEmpty()) {
            "SyncedSegment.subtitle.textZh must be non-empty"
        }
    }

    override fun equals(other: Any?): Boolean {
        if (this === other) return true
        if (other !is SyncedSegment) return false
        return startSec == other.startSec &&
            endSec == other.endSec &&
            pcm.contentEquals(other.pcm) &&
            subtitle == other.subtitle
    }

    override fun hashCode(): Int {
        var result = startSec.hashCode()
        result = 31 * result + endSec.hashCode()
        result = 31 * result + pcm.contentHashCode()
        result = 31 * result + subtitle.hashCode()
        return result
    }
}

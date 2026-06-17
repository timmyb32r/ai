package com.crimobile.infrastructure

/**
 * Thread-safe in-memory PCM ring buffer — 60 s capacity for s16le 16kHz mono.
 *
 * Stores raw PCM bytes sequentially.  All public methods are @Synchronized
 * for safe concurrent access from the HTTP-reader thread (writer) and
 * the playback thread (reader).
 *
 * The reader uses [readAtOffset] with a simple byte counter — no timeline
 * math.  Subtitle synchronisation uses AudioTrack.playbackHeadPosition +
 * [audioTimelineStartSec] (set once from the X-Audio-Timeline-Start header).
 */
class PCMRingBuffer {

    companion object {
        const val SAMPLE_RATE = 16000
        const val BYTES_PER_SAMPLE = 2
        const val CAPACITY_SECS = 60
        val BUFFER_SIZE = SAMPLE_RATE * BYTES_PER_SAMPLE * CAPACITY_SECS   // 1 920 000
        const val BYTES_PER_SEC = SAMPLE_RATE * BYTES_PER_SAMPLE           // 32 000
    }

    private val buffer = ByteArray(BUFFER_SIZE)
    private var writePos = 0
    private var totalWritten = 0L

    /** Server timeline position of the first byte in this buffer.
     *  Set once from X-Audio-Timeline-Start header by the PCM reader. */
    @Volatile
    var audioTimelineStartSec: Double = 0.0

    /** Total bytes ever written (monotonically increasing). */
    @Synchronized
    fun getTotalWritten(): Long = totalWritten

    // ── write ──────────────────────────────────────────────────────────

    @Synchronized
    fun write(pcm: ByteArray) = write(pcm, 0, pcm.size)

    @Synchronized
    fun write(pcm: ByteArray, offset: Int, length: Int) {
        var remaining = length
        var src = offset
        while (remaining > 0) {
            val chunk = minOf(remaining, BUFFER_SIZE - writePos)
            pcm.copyInto(buffer, writePos, src, src + chunk)
            writePos = (writePos + chunk) % BUFFER_SIZE
            src += chunk
            remaining -= chunk
            totalWritten += chunk
        }
    }

    // ── read ───────────────────────────────────────────────────────────

    /**
     * Reads up to [length] bytes starting at absolute byte offset
     * [byteOffset] (0 = first byte ever written).  Returns the number of
     * bytes actually read, or -1 if the requested range is not yet written
     * or has been evicted.
     */
    @Synchronized
    fun readAtOffset(dest: ByteArray, destOffset: Int, byteOffset: Long, length: Int): Int {
        if (totalWritten == 0L) return -1
        val oldest = (totalWritten - BUFFER_SIZE).coerceAtLeast(0L)
        if (byteOffset < oldest) return -1          // evicted
        if (byteOffset >= totalWritten) return -1   // not yet written

        val avail = minOf(length.toLong(), totalWritten - byteOffset, BUFFER_SIZE.toLong()).toInt()
        if (avail <= 0) return -1
        val bufPos = (byteOffset % BUFFER_SIZE).toInt()
        val firstChunk = minOf(avail, BUFFER_SIZE - bufPos)
        System.arraycopy(buffer, bufPos, dest, destOffset, firstChunk)
        if (firstChunk < avail) {
            System.arraycopy(buffer, 0, dest, destOffset + firstChunk, avail - firstChunk)
        }
        return avail
    }

    /** True when [byteOffset] is within the cached window. */
    @Synchronized
    fun isAvailable(byteOffset: Long): Boolean {
        if (totalWritten == 0L) return false
        val oldest = (totalWritten - BUFFER_SIZE).coerceAtLeast(0L)
        return byteOffset in oldest until totalWritten
    }

    /** Read range for pronounceWord — timeline-based random access. */
    @Synchronized
    fun readRange(timelineSec: Double, lengthBytes: Int): ByteArray? {
        if (totalWritten == 0L) return null
        val byteOff = ((timelineSec - audioTimelineStartSec) * BYTES_PER_SEC).toLong()
        val oldest = (totalWritten - BUFFER_SIZE).coerceAtLeast(0L)
        if (byteOff < oldest || byteOff >= totalWritten) return null
        val actualLen = minOf(lengthBytes, (totalWritten - byteOff).toInt(), BUFFER_SIZE)
        if (actualLen <= 0) return null
        val result = ByteArray(actualLen)
        readAtOffset(result, 0, byteOff, actualLen)
        return result
    }

    @Synchronized
    fun clear() {
        writePos = 0
        totalWritten = 0L
    }
}

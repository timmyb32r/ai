package com.crimobile.player

import androidx.media3.exoplayer.hls.playlist.DefaultHlsPlaylistParserFactory
import androidx.media3.exoplayer.hls.playlist.HlsMediaPlaylist
import androidx.media3.exoplayer.hls.playlist.HlsMultivariantPlaylist
import androidx.media3.exoplayer.hls.playlist.HlsPlaylist
import androidx.media3.exoplayer.hls.playlist.HlsPlaylistParserFactory
import androidx.media3.exoplayer.upstream.ParsingLoadable
import java.io.IOException

/** Capture the same bytes Media3 parsed, not a newer independently fetched playlist. */
class TimelinePlaylistParserFactory : HlsPlaylistParserFactory {
    private val delegate = DefaultHlsPlaylistParserFactory()
    private val maps = linkedMapOf<String, HlsTimelineMap>()
    private fun key(p: HlsMediaPlaylist) = "${p.baseUri}|${p.mediaSequence}|${p.segments.firstOrNull()?.url}|${p.segments.lastOrNull()?.url}|${p.durationUs}"

    @Synchronized fun forPlaylist(p: HlsMediaPlaylist): HlsTimelineMap? = maps[key(p)]

    override fun createPlaylistParser() = wrap(delegate.createPlaylistParser())
    override fun createPlaylistParser(multivariantPlaylist: HlsMultivariantPlaylist, previousMediaPlaylist: HlsMediaPlaylist?) =
        wrap(delegate.createPlaylistParser(multivariantPlaylist, previousMediaPlaylist))

    private fun wrap(parser: ParsingLoadable.Parser<HlsPlaylist>) = ParsingLoadable.Parser<HlsPlaylist> { uri, input ->
        val buffer = java.io.ByteArrayOutputStream()
        val chunk = ByteArray(8192)
        while (true) {
            val count = input.read(chunk)
            if (count < 0) break
            if (buffer.size() + count > 2 * 1024 * 1024) throw IOException("HLS playlist exceeds 2 MiB")
            buffer.write(chunk, 0, count)
        }
        val bytes = buffer.toByteArray()
        val result = parser.parse(uri, bytes.inputStream())
        if (result is HlsMediaPlaylist) {
            HlsTimelineMap.parse(bytes.toString(Charsets.UTF_8))?.let { map ->
                synchronized(this) {
                    maps[key(result)] = map
                    while (maps.size > 16) maps.remove(maps.keys.first())
                }
            }
        }
        result
    }
}

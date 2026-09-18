package pipeline

// libmp3lame's MPEG-2 output at 16 kHz has 576 samples of encoder delay,
// followed by the MP3 decoder's 528+1 sample synthesis delay. MPEG-TS does not
// preserve the skip-sample side data which a gapless MP3 container would use.
//
// FFmpeg documents this combination in libavcodec/libmp3lame.c:
// https://github.com/FFmpeg/FFmpeg/blob/n8.1/libavcodec/libmp3lame.c
//
// This is a codec-chain property, applied once per capture generation, never
// once per segment. TestEncodedPCMMatchesActualHLS verifies the number against
// the actual encoder/decoder, including first and later playlist segments.
const mp3PrimingSamples int64 = 576 + 528 + 1
const mp3FrameSamples int64 = 576

// encodedPCM returns samples aligned to [start,end) on the encoded HLS sample
// clock. The ring contains the pre-encoder PCM from the same asplit filter.
// Negative source coordinates are initial codec silence. A finalized stream may
// contain at most one frame of codec tail padding beyond the captured input;
// it must never manufacture seconds of missing lookahead or overwritten audio.
//
// Callers pass media coordinates, not raw-ring coordinates. For final capture,
// they should omit unavailable ASR lookahead while retaining the complete media
// segment interval. Output always has exactly end-start samples on success.
func encodedPCM(r *pcmRing, start, end int64, final bool) ([]float32, bool) {
	if r == nil || start < 0 || end <= start {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	capacity := int64(len(r.data))
	if capacity == 0 || end-start > capacity+mp3PrimingSamples+mp3FrameSamples {
		return nil, false
	}
	sourceStart := start - mp3PrimingSamples
	sourceEnd := end - mp3PrimingSamples
	if sourceEnd > r.total {
		if !final || sourceEnd-r.total > mp3FrameSamples {
			return nil, false
		}
	}
	copyStart := max(sourceStart, int64(0))
	copyEnd := min(sourceEnd, r.total)
	if copyEnd > copyStart && copyStart < r.total-capacity {
		return nil, false
	}
	out := make([]float32, int(end-start))
	for sample := copyStart; sample < copyEnd; sample++ {
		out[sample-sourceStart] = r.data[sample%capacity]
	}
	return out, true
}

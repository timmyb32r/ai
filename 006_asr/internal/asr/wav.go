package asr

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
)

// writeWAV writes PCM float32 samples as a 16-bit PCM WAV file and returns the file path.
func writeWAV(samples []float32, sampleRate int) (string, error) {
	f, err := os.CreateTemp("", "asr-*.wav")
	if err != nil {
		return "", err
	}
	defer f.Close()

	numSamples := len(samples)
	byteRate := sampleRate * 2 // 16-bit = 2 bytes per sample
	dataSize := numSamples * 2
	fileSize := 36 + dataSize

	// RIFF header
	writeLE(f, []byte("RIFF"), uint32(fileSize), []byte("WAVE"))

	// fmt chunk
	writeLE(f,
		[]byte("fmt "),
		uint32(16),          // chunk size
		uint16(1),           // PCM format
		uint16(1),           // mono
		uint32(sampleRate),  // sample rate
		uint32(byteRate),    // byte rate
		uint16(2),           // block align
		uint16(16),          // bits per sample
	)

	// data chunk
	writeLE(f, []byte("data"), uint32(dataSize))

	// Convert float32 [-1.0, 1.0] to int16
	for _, s := range samples {
		if s > 1.0 {
			s = 1.0
		}
		if s < -1.0 {
			s = -1.0
		}
		val := int16(s * math.MaxInt16)
		binary.Write(f, binary.LittleEndian, val)
	}

	return filepath.Abs(f.Name())
}

func writeLE(f *os.File, args ...interface{}) {
	for _, arg := range args {
		switch v := arg.(type) {
		case []byte:
			f.Write(v)
		case uint32:
			binary.Write(f, binary.LittleEndian, v)
		case uint16:
			binary.Write(f, binary.LittleEndian, v)
		}
	}
}

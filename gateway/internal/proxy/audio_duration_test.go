package proxy_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/ifixtelecom/gpu-ifix/gateway/internal/proxy"
)

// buildWAV builds a minimal canonical PCM WAV with the given parameters and
// sampleCount frames of silence, so the duration is exactly
// sampleCount / sampleRate seconds.
func buildWAV(sampleRate uint32, channels, bitsPerSample uint16, sampleCount uint32) []byte {
	bytesPerSample := uint32(bitsPerSample) / 8
	dataBytes := sampleCount * uint32(channels) * bytesPerSample

	var b bytes.Buffer
	// RIFF header
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+dataBytes)) // chunk size
	b.WriteString("WAVE")
	// fmt chunk
	b.WriteString("fmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16)) // PCM fmt size
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))  // audioFormat = PCM
	_ = binary.Write(&b, binary.LittleEndian, channels)
	_ = binary.Write(&b, binary.LittleEndian, sampleRate)
	byteRate := sampleRate * uint32(channels) * bytesPerSample
	_ = binary.Write(&b, binary.LittleEndian, byteRate)
	blockAlign := channels * uint16(bytesPerSample)
	_ = binary.Write(&b, binary.LittleEndian, blockAlign)
	_ = binary.Write(&b, binary.LittleEndian, bitsPerSample)
	// data chunk
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, dataBytes)
	b.Write(make([]byte, dataBytes)) // silence
	return b.Bytes()
}

// TestDeriveAudioSecondsWAVKnownDuration: a 16-bit mono 16 kHz WAV with
// 32000 frames is exactly 2.0 seconds. Parsed exactly from the RIFF header.
func TestDeriveAudioSecondsWAVKnownDuration(t *testing.T) {
	// 16 kHz mono 16-bit, 32000 samples → 2.0s.
	wav := buildWAV(16000, 1, 16, 32000)
	got := proxy.DeriveAudioSeconds(wav, "audio/wav")
	want := 2.0
	if diff := got - want; diff > 0.01 || diff < -0.01 {
		t.Fatalf("WAV duration: want ~%.2f, got %.4f", want, got)
	}

	// Stereo 44.1 kHz 16-bit, 44100 frames → exactly 1.0s.
	wav2 := buildWAV(44100, 2, 16, 44100)
	got2 := proxy.DeriveAudioSeconds(wav2, "application/octet-stream")
	if diff := got2 - 1.0; diff > 0.01 || diff < -0.01 {
		t.Fatalf("stereo WAV duration: want ~1.00, got %.4f", got2)
	}
}

// TestDeriveAudioSecondsCompressedEstimate: a non-WAV mime returns a non-zero
// byte/bitrate estimate for a non-empty body.
func TestDeriveAudioSecondsCompressedEstimate(t *testing.T) {
	// 16 KiB of "mp3" bytes at 128 kbps ≈ 16384*8/128000 = 1.024s.
	body := bytes.Repeat([]byte{0xFF, 0xFB, 0x90, 0x00}, 4096) // 16384 bytes
	for _, mime := range []string{"audio/mpeg", "audio/ogg"} {
		got := proxy.DeriveAudioSeconds(body, mime)
		if got <= 0 {
			t.Fatalf("compressed estimate (%s): want >0, got %.4f", mime, got)
		}
		want := float64(len(body)) * 8.0 / (128.0 * 1000.0)
		if diff := got - want; diff > 0.01 || diff < -0.01 {
			t.Fatalf("compressed estimate (%s): want ~%.4f, got %.4f", mime, want, got)
		}
	}
}

// TestDeriveAudioSecondsEmptyAndGarbage: empty input → 0 (no panic); garbage
// non-WAV bytes still yield a non-negative estimate; a truncated RIFF magic
// with no chunks falls back to the byte estimate (non-zero, no panic).
func TestDeriveAudioSecondsEmptyAndGarbage(t *testing.T) {
	if got := proxy.DeriveAudioSeconds(nil, "audio/wav"); got != 0 {
		t.Fatalf("empty input: want 0, got %.4f", got)
	}
	if got := proxy.DeriveAudioSeconds([]byte{}, ""); got != 0 {
		t.Fatalf("zero-length input: want 0, got %.4f", got)
	}
	// Garbage bytes, non-WAV → byte estimate, must not panic, >=0.
	if got := proxy.DeriveAudioSeconds([]byte{0x00, 0x01, 0x02, 0x03}, "application/octet-stream"); got < 0 {
		t.Fatalf("garbage input: want >=0, got %.4f", got)
	}
	// "RIFF....WAVE" magic but no fmt/data chunks → wav parse fails, falls
	// back to byte estimate (non-zero), must not panic.
	truncated := append([]byte("RIFF"), append([]byte{0, 0, 0, 0}, []byte("WAVE")...)...)
	if got := proxy.DeriveAudioSeconds(truncated, "audio/wav"); got < 0 {
		t.Fatalf("truncated WAV: want >=0, got %.4f", got)
	}
}

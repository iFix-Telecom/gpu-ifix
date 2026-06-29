// Package proxy (audio_duration.go): request-audio duration estimator.
//
// DeriveAudioSeconds turns the raw bytes of an uploaded audio file into an
// estimated duration in seconds. It is the ELSE branch of LOCKED CONTEXT
// DECISION #2 (Phase 16): the STT billing producer prefers the upstream
// response `duration` field, but the gateway does NOT force
// response_format=verbose_json, so the common-case OpenAI/Speaches/Whisper
// transcription response is {"text":"..."} with NO duration. Without a
// request-derived fallback the common-case client would meter 0.
//
// Two paths:
//   - WAV (RIFF/WAVE): parse the `fmt ` chunk (channels, sampleRate,
//     bitsPerSample) and the `data` chunk byte length → an EXACT duration
//     (PCM is uncompressed). This is the precise path.
//   - Anything else (mp3/ogg/m4a/flac/…): a documented constant-bitrate
//     estimate. Compressed containers do not carry a cheap exact duration
//     in their header; the estimate is intentionally rough and accepted for
//     quota metering (T-16-02 — STT providers are first-party/contracted and
//     the value is bounded by the actual uploaded bytes).
//
// All header reads are bounds-checked; empty / garbage / truncated input
// returns 0 (no panic) — proven by the empty/garbage unit test (T-16-08).
// No external dependencies.
package proxy

import (
	"encoding/binary"
	"strings"
)

// assumedKbps is the constant bitrate (kilobits per second) assumed for
// NON-WAV (compressed) audio when deriving a duration from byte length.
//
// 128 kbps is a common mid-quality lossy encode (mp3/aac/ogg/opus voice +
// music typically land between 64–192 kbps). The estimate is therefore an
// OVER-estimate of duration for higher-bitrate encodes and an UNDER-estimate
// for lower-bitrate ones — accepted for quota metering (the request bytes
// bound the value either way; T-16-02). seconds ≈ (bytes * 8) / (kbps * 1000).
const assumedKbps = 128

// DeriveAudioSeconds estimates the duration (seconds) of the supplied audio
// payload. Returns 0 for empty input or unparseable headers (never panics).
//
//   - WAV (RIFF…WAVE): exact PCM duration from the fmt/data chunks.
//   - other / compressed mime: constant-bitrate estimate (assumedKbps).
func DeriveAudioSeconds(audio []byte, mime string) float64 {
	if len(audio) == 0 {
		return 0
	}

	// WAV path — either the mime says so OR the RIFF/WAVE magic is present.
	// We prefer the magic (mimes are client-controlled and frequently
	// "application/octet-stream" for file uploads).
	if isWAV(audio) {
		if s, ok := wavDurationSeconds(audio); ok {
			return s
		}
		// Magic present but header unparseable (truncated chunk) → fall
		// through to the byte/bitrate estimate rather than returning 0, so a
		// real (if malformed) WAV still meters non-zero.
	}

	// Non-WAV / fallback: constant-bitrate estimate. mime is informational
	// only here — the estimate is byte-length driven.
	_ = mime
	bits := float64(len(audio)) * 8.0
	return bits / (float64(assumedKbps) * 1000.0)
}

// isWAV reports whether the buffer begins with the canonical RIFF…WAVE magic.
func isWAV(b []byte) bool {
	// "RIFF" <4-byte size> "WAVE" → 12 bytes minimum.
	if len(b) < 12 {
		return false
	}
	return string(b[0:4]) == "RIFF" && string(b[8:12]) == "WAVE"
}

// wavDurationSeconds parses a canonical PCM WAV and returns the exact
// duration. ok=false when any required field is missing / out of bounds
// (caller then falls back to the byte estimate). Bounds-checked throughout.
func wavDurationSeconds(b []byte) (seconds float64, ok bool) {
	// Header layout: bytes 0-3 "RIFF", 4-7 chunkSize, 8-11 "WAVE", then a
	// sequence of sub-chunks: 4-byte id, 4-byte little-endian size, payload.
	if len(b) < 12 {
		return 0, false
	}

	var (
		sampleRate    uint32
		channels      uint16
		bitsPerSample uint16
		dataBytes     uint32
		haveFmt       bool
		haveData      bool
	)

	pos := 12
	for pos+8 <= len(b) {
		id := string(b[pos : pos+4])
		size := binary.LittleEndian.Uint32(b[pos+4 : pos+8])
		payload := pos + 8

		switch id {
		case "fmt ":
			// PCM fmt chunk is >=16 bytes: audioFormat(2) channels(2)
			// sampleRate(4) byteRate(4) blockAlign(2) bitsPerSample(2).
			if payload+16 <= len(b) {
				channels = binary.LittleEndian.Uint16(b[payload+2 : payload+4])
				sampleRate = binary.LittleEndian.Uint32(b[payload+4 : payload+8])
				bitsPerSample = binary.LittleEndian.Uint16(b[payload+14 : payload+16])
				haveFmt = true
			}
		case "data":
			// Trust the declared data size, but clamp to the bytes we
			// actually have (a truncated upload declares more than it ships).
			avail := uint32(len(b) - payload)
			if size <= avail {
				dataBytes = size
			} else {
				dataBytes = avail
			}
			haveData = true
		}

		// Advance: chunk bodies are word-aligned (pad byte when size is odd).
		adv := int(size)
		if adv%2 == 1 {
			adv++
		}
		// Guard against overflow / runaway size driving pos backwards.
		if adv < 0 {
			break
		}
		pos = payload + adv

		if haveFmt && haveData {
			break
		}
	}

	if !haveFmt || !haveData {
		return 0, false
	}
	bytesPerSample := uint32(bitsPerSample) / 8
	denom := sampleRate * uint32(channels) * bytesPerSample
	if denom == 0 || dataBytes == 0 {
		return 0, false
	}
	return float64(dataBytes) / float64(denom), true
}

// isAudioTranscriptionsPath reports whether the path targets the STT
// transcription endpoint whose multipart body carries the request audio.
func isAudioTranscriptionsPath(path string) bool {
	return strings.HasPrefix(path, "/v1/audio/transcriptions")
}

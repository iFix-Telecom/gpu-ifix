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
// Paths:
//   - WAV (RIFF/WAVE): parse the `fmt ` chunk (channels, sampleRate,
//     bitsPerSample) and the `data` chunk byte length → an EXACT duration
//     (PCM is uncompressed). This is the precise path.
//   - MP3 (Layer III) and Ogg/Opus (quick 261108-pnd): frame header bitrate
//     or Xing/Info frame count, and last-page granule position.
//   - Anything else (m4a/flac/…): a documented constant-bitrate
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
	"bytes"
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

	// Quick 261108-pnd: MP3 (frame header + Xing/Info) and Ogg/Opus (last
	// granule position) carry a cheap exact-enough duration. Before, every
	// non-WAV file fell to the 128 kbps estimate below, and the ~8 kbps voip
	// call recordings metered ~16x short (27 s billed vs 435 s at Groq).
	if s, ok := compressedDurationSeconds(audio); ok {
		return s
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

// compressedDurationSeconds returns the duration of an MP3 (Layer III) or
// Ogg/Opus payload read from its own headers, or (0, false) when the bytes
// are neither or the headers do not validate.
//
// The header value is client-controlled, so it is clamped by the payload
// size: never shorter than the pre-existing assumedKbps estimate (a forged
// header can only RAISE the metered duration relative to the old behaviour,
// never shrink it — no quota/billing bypass) and never longer than a 4 kbps
// encode of the same bytes.
func compressedDurationSeconds(b []byte) (float64, bool) {
	s, ok := oggOpusDurationSeconds(b)
	if !ok {
		s, ok = mp3DurationSeconds(b)
	}
	if !ok {
		return 0, false
	}
	bits := float64(len(b)) * 8
	return min(max(s, bits/(compressedMaxKbps*1000)), bits/(compressedMinKbps*1000)), true
}

// Bitrate bounds for the header clamp (kbps). The upper bound equals
// assumedKbps on purpose: the size-only estimate is the floor.
const (
	compressedMinKbps = 4
	compressedMaxKbps = assumedKbps
)

var (
	mp3V1L3Kbps  = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	mp3V2L3Kbps  = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}
	mp3RatesV1   = [3]int{44100, 48000, 32000}
	mp3RatesV2   = [3]int{22050, 24000, 16000}
	mp3RatesV25  = [3]int{11025, 12000, 8000}
	mp3ScanLimit = 64 << 10 // bytes searched for the first frame after ID3
)

// mp3Frame is a parsed MPEG audio Layer III frame header.
type mp3Frame struct {
	mpeg1      bool
	mono       bool
	kbps       int
	sampleRate int
	length     int // frame length in bytes, padding included
}

func parseMP3Frame(h []byte) (mp3Frame, bool) {
	if len(h) < 4 || h[0] != 0xFF || h[1]&0xE0 != 0xE0 {
		return mp3Frame{}, false
	}
	version := (h[1] >> 3) & 3 // 3=MPEG1 2=MPEG2 0=MPEG2.5 1=reserved
	layer := (h[1] >> 1) & 3   // 1=Layer III
	if version == 1 || layer != 1 {
		return mp3Frame{}, false
	}
	bi, si := (h[2]>>4)&0xF, (h[2]>>2)&3
	if si == 3 {
		return mp3Frame{}, false
	}
	f := mp3Frame{mpeg1: version == 3, mono: (h[3]>>6)&3 == 3}
	switch version {
	case 3:
		f.kbps, f.sampleRate = mp3V1L3Kbps[bi], mp3RatesV1[si]
	case 2:
		f.kbps, f.sampleRate = mp3V2L3Kbps[bi], mp3RatesV2[si]
	default:
		f.kbps, f.sampleRate = mp3V2L3Kbps[bi], mp3RatesV25[si]
	}
	if f.kbps == 0 {
		return mp3Frame{}, false // free-format / bad index
	}
	coef := 72
	if f.mpeg1 {
		coef = 144
	}
	f.length = coef*f.kbps*1000/f.sampleRate + int((h[2]>>1)&1)
	return f, f.length > 4
}

func (f mp3Frame) samplesPerFrame() int {
	if f.mpeg1 {
		return 1152
	}
	return 576
}

// mp3DurationSeconds reads an MP3: skips an ID3v2 tag, finds the first frame
// whose successor also syncs (guards against false 0xFF matches), then uses
// the Xing/Info frame count when present (VBR) or the CBR bitrate otherwise.
func mp3DurationSeconds(b []byte) (float64, bool) {
	start := 0
	if len(b) >= 10 && string(b[0:3]) == "ID3" {
		size := int(b[6]&0x7F)<<21 | int(b[7]&0x7F)<<14 | int(b[8]&0x7F)<<7 | int(b[9]&0x7F)
		start = 10 + size
		if b[5]&0x10 != 0 {
			start += 10 // footer
		}
	}
	limit := start + mp3ScanLimit
	if limit > len(b)-4 {
		limit = len(b) - 4
	}
	for i := start; i < limit; i++ {
		f, ok := parseMP3Frame(b[i:])
		if !ok {
			continue
		}
		// The successor must sync too; a lone frame counts only when it ends
		// exactly at EOF (single-frame file), never "near the end".
		next := i + f.length
		if next+4 <= len(b) {
			if _, ok2 := parseMP3Frame(b[next:]); !ok2 {
				continue
			}
		} else if next != len(b) {
			continue
		}
		if frames, ok := xingFrames(b[i:], f); ok && frames > 0 {
			return float64(frames) * float64(f.samplesPerFrame()) / float64(f.sampleRate), true
		}
		return float64(len(b)-i) * 8 / float64(f.kbps*1000), true
	}
	return 0, false
}

// xingFrames reads the frame count of a Xing/Info header in the first frame.
func xingFrames(frame []byte, f mp3Frame) (int, bool) {
	side := 17 // MPEG1 stereo 32, mono 17; MPEG2/2.5 stereo 17, mono 9
	switch {
	case f.mpeg1 && !f.mono:
		side = 32
	case !f.mpeg1 && f.mono:
		side = 9
	}
	off := 4 + side
	if len(frame) < off+12 {
		return 0, false
	}
	tag := string(frame[off : off+4])
	if tag != "Xing" && tag != "Info" {
		return 0, false
	}
	flags := binary.BigEndian.Uint32(frame[off+4 : off+8])
	if flags&1 == 0 {
		return 0, false
	}
	return int(binary.BigEndian.Uint32(frame[off+8 : off+12])), true
}

// oggOpusDurationSeconds reads an Ogg/Opus stream: (last page granule
// position - pre-skip) / 48 kHz, the Opus reference clock.
func oggOpusDurationSeconds(b []byte) (float64, bool) {
	if len(b) < 47 || string(b[0:4]) != "OggS" {
		return 0, false
	}
	head := bytes.Index(b[:min(len(b), 200)], []byte("OpusHead"))
	if head < 0 || head+12 > len(b) {
		return 0, false
	}
	preSkip := int64(binary.LittleEndian.Uint16(b[head+10 : head+12]))
	last := bytes.LastIndex(b, []byte("OggS"))
	if last < 0 || last+14 > len(b) {
		return 0, false
	}
	granule := int64(binary.LittleEndian.Uint64(b[last+6 : last+14]))
	if granule <= preSkip {
		return 0, false
	}
	return float64(granule-preSkip) / 48000, true
}

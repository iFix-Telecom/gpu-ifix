package proxy

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// Quick 261108-pnd: MP3 and Ogg/Opus durations read from their own headers.
// Fixtures generated with ffmpeg; expected values from ffprobe.
func TestCompressedDurationSeconds_Fixtures(t *testing.T) {
	cases := []struct {
		file string
		want float64
	}{
		{"cbr8k.mp3", 7.2},           // 8 kbps mono 8 kHz (voip-like), Info header
		{"cbr8k_id3.mp3", 7.2},       // same + ID3v2 tag in front
		{"vbr.mp3", 6.06},            // VBR, Xing header
		{"cbr64k_stereo.mp3", 4.049}, // MPEG1 stereo
		{"voice.ogg", 5.0065},        // Ogg/Opus
	}
	for _, c := range cases {
		b, err := os.ReadFile(filepath.Join("testdata", "audio", c.file))
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		got := DeriveAudioSeconds(b, "audio/mpeg")
		if math.Abs(got-c.want) > 0.1 {
			t.Errorf("%s: DeriveAudioSeconds = %.3f, want %.3f ±0.1", c.file, got, c.want)
		}
		// The STT header budget uses the same exact value.
		if up := sttAudioUpperSeconds(b, got); math.Abs(up-c.want) > 0.1 {
			t.Errorf("%s: sttAudioUpperSeconds = %.3f, want %.3f", c.file, up, c.want)
		}
	}
}

// CBR without Xing: bitrate from the frame header, not the 128 kbps guess.
func TestMP3DurationSeconds_CBRNoXing(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "audio", "cbr8k.mp3"))
	if err != nil {
		t.Fatal(err)
	}
	f, ok := parseMP3Frame(b[findSync(t, b):])
	if !ok {
		t.Fatal("first frame should parse")
	}
	// Drop the Info frame: the rest is plain CBR frames.
	rest := b[findSync(t, b)+f.length:]
	got, ok := mp3DurationSeconds(rest)
	want := float64(len(rest)) * 8 / 8000
	if !ok || math.Abs(got-want) > 0.01 {
		t.Fatalf("CBR no-Xing = %.3f ok=%v, want %.3f", got, ok, want)
	}
}

func findSync(t *testing.T, b []byte) int {
	t.Helper()
	for i := 0; i+4 <= len(b); i++ {
		if _, ok := parseMP3Frame(b[i:]); ok {
			return i
		}
	}
	t.Fatal("no mp3 frame")
	return 0
}

// Garbage, truncated and non-audio input never parse (and never panic).
func TestCompressedDurationSeconds_Garbage(t *testing.T) {
	inputs := [][]byte{
		nil,
		[]byte("hello world"),
		{0xFF, 0xFB},                   // truncated header
		{0xFF, 0xFF, 0xFF, 0xFF, 0xFF}, // bad version/layer bits
		append([]byte("OggS"), make([]byte, 60)...),                // Ogg without OpusHead
		append([]byte("ID3\x03\x00\x00\x7f\x7f\x7f\x7f"), 1, 2, 3), // ID3 size past end
	}
	for i, in := range inputs {
		if s, ok := compressedDurationSeconds(in); ok {
			t.Errorf("input %d parsed as %.3f s, want no parse", i, s)
		}
	}
}

// A forged header cannot push the duration outside what the payload size
// allows (security review: quota/billing bypass).
func TestCompressedDurationSeconds_ClampedByPayload(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "audio", "voice.ogg"))
	if err != nil {
		t.Fatal(err)
	}
	forged := append([]byte(nil), b...)
	last := bytes.LastIndex(forged, []byte("OggS"))
	// granule = pre-skip + 1 sample → header claims ~0 s.
	head := bytes.Index(forged, []byte("OpusHead"))
	pre := uint64(binary.LittleEndian.Uint16(forged[head+10 : head+12]))
	binary.LittleEndian.PutUint64(forged[last+6:last+14], pre+1)
	got, ok := compressedDurationSeconds(forged)
	floor := float64(len(forged)) * 8 / (compressedMaxKbps * 1000)
	if !ok || got < floor-1e-9 {
		t.Fatalf("forged short header = %.4f ok=%v, want >= %.4f (512 kbps floor)", got, ok, floor)
	}
	// Huge granule → capped at the 4 kbps ceiling.
	binary.LittleEndian.PutUint64(forged[last+6:last+14], 1<<40)
	got, _ = compressedDurationSeconds(forged)
	ceil := float64(len(forged)) * 8 / (compressedMinKbps * 1000)
	if got > ceil+1e-9 {
		t.Fatalf("forged long header = %.1f, want <= %.1f (4 kbps ceiling)", got, ceil)
	}
}

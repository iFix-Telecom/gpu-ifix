package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"mime/multipart"
	"testing"
)

// makeTestWav builds a minimal valid RIFF/WAVE (PCM 16-bit mono 8 kHz) with
// `seconds` of silence — the same shape voip-api call recordings arrive in.
func makeTestWav(seconds int) []byte {
	const sampleRate = 8000
	dataLen := seconds * sampleRate * 2 // 16-bit mono
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+dataLen))
	b.WriteString("WAVE")
	b.WriteString("fmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&b, binary.LittleEndian, uint16(1)) // mono
	binary.Write(&b, binary.LittleEndian, uint32(sampleRate))
	binary.Write(&b, binary.LittleEndian, uint32(sampleRate*2)) // byte rate
	binary.Write(&b, binary.LittleEndian, uint16(2))            // block align
	binary.Write(&b, binary.LittleEndian, uint16(16))           // bits
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(dataLen))
	b.Write(make([]byte, dataLen))
	return b.Bytes()
}

func TestIsWavBytes(t *testing.T) {
	if !isWavBytes(makeTestWav(1)) {
		t.Fatal("valid wav not detected")
	}
	if isWavBytes([]byte("ID3\x04not a wav at all........")) {
		t.Fatal("mp3-ish bytes detected as wav")
	}
	if isWavBytes([]byte("RIFF")) {
		t.Fatal("truncated header detected as wav")
	}
}

// TestShouldTranscodeSTTAudio_Gates: small files and non-WAV never transcode;
// the env kill-switch disables the feature.
func TestShouldTranscodeSTTAudio_Gates(t *testing.T) {
	small := makeTestWav(10) // ~160 KB < 1 MiB floor
	if shouldTranscodeSTTAudio(small) {
		t.Fatal("small wav must not transcode")
	}
	big := makeTestWav(120) // ~1.9 MB
	notWav := make([]byte, len(big))
	copy(notWav, big)
	copy(notWav[0:4], "XXXX")
	if shouldTranscodeSTTAudio(notWav) {
		t.Fatal("non-wav must not transcode")
	}
	t.Setenv("STT_TRANSCODE_DISABLED", "1")
	if shouldTranscodeSTTAudio(big) {
		t.Fatal("kill-switch env must disable transcode")
	}
}

// TestTranscodeWavToOpus: requires ffmpeg on PATH (CI ubuntu runners and the
// gateway image both ship it); skipped where absent so the suite stays green
// on minimal dev machines.
func TestTranscodeWavToOpus(t *testing.T) {
	if resolveFFmpegPath() == "" {
		t.Skip("ffmpeg not available")
	}
	wav := makeTestWav(120) // ~1.9 MB PCM
	opus, err := transcodeWavToOpus(context.Background(), wav)
	if err != nil {
		t.Fatalf("transcode failed: %v", err)
	}
	if len(opus) == 0 || len(opus) >= len(wav) {
		t.Fatalf("expected compressed output, wav=%d opus=%d", len(wav), len(opus))
	}
	if !bytes.Equal(opus[0:4], []byte("OggS")) {
		t.Fatalf("output is not an Ogg stream: % x", opus[0:4])
	}
}

// TestReplaceMultipartFilePart: the file part is swapped (payload, filename,
// content-type) while every other field survives verbatim.
func TestReplaceMultipartFilePart(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "recording.mp3")
	fw.Write([]byte("OLD-WAV-BYTES"))
	mw.WriteField("model", "whisper")
	mw.WriteField("response_format", "verbose_json")
	mw.Close()

	newBuf, newBoundary, ok := replaceMultipartFilePart(
		body.Bytes(), mw.Boundary(), "recording.ogg", "audio/ogg", []byte("NEW-OPUS"))
	if !ok {
		t.Fatal("replace failed")
	}
	mr := multipart.NewReader(bytes.NewReader(newBuf), newBoundary)
	got := map[string]string{}
	var fileName, fileCT, fileData string
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reparse: %v", err)
		}
		data, _ := io.ReadAll(p)
		if p.FormName() == "file" {
			fileName = p.FileName()
			fileCT = p.Header.Get("Content-Type")
			fileData = string(data)
		} else {
			got[p.FormName()] = string(data)
		}
	}
	if fileData != "NEW-OPUS" || fileName != "recording.ogg" || fileCT != "audio/ogg" {
		t.Fatalf("file part wrong: name=%q ct=%q data=%q", fileName, fileCT, fileData)
	}
	if got["model"] != "whisper" || got["response_format"] != "verbose_json" {
		t.Fatalf("other fields corrupted: %v", got)
	}
}

// TestReplaceMultipartFilePart_NoFilePart: body without a "file" part is
// returned unchanged (ok=false) — fail-open contract.
func TestReplaceMultipartFilePart_NoFilePart(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("model", "whisper")
	mw.Close()
	_, _, ok := replaceMultipartFilePart(body.Bytes(), mw.Boundary(), "x.ogg", "audio/ogg", []byte("Y"))
	if ok {
		t.Fatal("expected ok=false when no file part exists")
	}
}

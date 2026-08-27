// Package proxy (stt_transcode.go): request-side audio compression for STT.
//
// Call recordings arrive as raw PCM WAV (8 kHz 16-bit mono ≈ 16 KB/s — a
// 10-minute call is ~9.6 MB). Every STT upstream decodes compressed audio just
// as well, and the tier-1 providers cap the upload size (openai/groq 25 MB,
// gemini inline ~20 MB), so shipping raw PCM wastes bandwidth, upload latency
// and headroom on every hop of the cascade. This file transcodes large WAV
// uploads to Ogg/Opus (~24 kbps mono ≈ 16x smaller) INSIDE the gateway, so
// every tenant benefits without touching any client (decisão Pedro 2026-08-27,
// after the local-stt pod's full disk turned >1 MiB uploads into HTTP 400 —
// see .planning/debug/stt-pod-disk-full-400.md).
//
// Fail-open by design: no ffmpeg binary, transcode error, timeout, or an
// output that is somehow LARGER than the input → forward the original bytes
// untouched. Transcoding is an optimization, never a gate.
package proxy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"os/exec"
	"sync"
	"time"
)

// sttTranscodeMinBytes: WAV files at or below this size are forwarded as-is.
// 1 MiB ≈ 65s of 8 kHz PCM — short dictation clips gain nothing from a
// transcode round-trip, and this floor keeps the ffmpeg exec off the hot path
// for the high-frequency small requests.
const sttTranscodeMinBytes = 1 << 20

// sttTranscodeTimeout bounds the ffmpeg exec. Opus-encoding an hour of 8 kHz
// PCM takes single-digit seconds on any CPU; 30s means "wedged", not "slow".
const sttTranscodeTimeout = 30 * time.Second

var (
	ffmpegPathOnce sync.Once
	ffmpegPath     string
)

// resolveFFmpegPath locates ffmpeg once. FFMPEG_PATH overrides; otherwise
// $PATH. Empty string = unavailable (transcode silently disabled).
func resolveFFmpegPath() string {
	ffmpegPathOnce.Do(func() {
		if p := os.Getenv("FFMPEG_PATH"); p != "" {
			if _, err := os.Stat(p); err == nil {
				ffmpegPath = p
			}
			return
		}
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			ffmpegPath = p
		}
	})
	return ffmpegPath
}

// sttTranscodeEnabled: STT_TRANSCODE_DISABLED=1/true kills the feature without
// a rebuild (operational escape hatch; read per-request so a container restart
// is not required when the env is injected via orchestrator update).
func sttTranscodeEnabled() bool {
	v := os.Getenv("STT_TRANSCODE_DISABLED")
	return v != "1" && v != "true"
}

// isWavBytes reports whether data carries a RIFF/WAVE header. Detection is on
// CONTENT, never the multipart Content-Type — clients routinely lie about the
// mime (the DiscLight pipeline uploads WAV named "recording.mp3").
func isWavBytes(data []byte) bool {
	return len(data) >= 12 &&
		bytes.Equal(data[0:4], []byte("RIFF")) &&
		bytes.Equal(data[8:12], []byte("WAVE"))
}

// shouldTranscodeSTTAudio gates the transcode: feature enabled, ffmpeg present,
// file is WAV and big enough to be worth the exec.
func shouldTranscodeSTTAudio(fileBytes []byte) bool {
	return sttTranscodeEnabled() &&
		len(fileBytes) > sttTranscodeMinBytes &&
		isWavBytes(fileBytes) &&
		resolveFFmpegPath() != ""
}

// transcodeWavToOpus runs ffmpeg wav→ogg/opus fully through pipes (no temp
// files — distroless has no shell and needs no disk). 16 kHz mono 24 kbps is
// transparent for 8 kHz telephony audio and ~16x smaller than the PCM input.
func transcodeWavToOpus(ctx context.Context, wav []byte) ([]byte, error) {
	path := resolveFFmpegPath()
	if path == "" {
		return nil, errors.New("stt transcode: ffmpeg unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, sttTranscodeTimeout)
	defer cancel()

	var out, errBuf bytes.Buffer
	cmd := exec.CommandContext(ctx, path,
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "wav", "-i", "pipe:0",
		"-ac", "1", "-ar", "16000",
		"-c:a", "libopus", "-b:a", "24k",
		"-f", "ogg", "pipe:1",
	)
	cmd.Stdin = bytes.NewReader(wav)
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		msg := errBuf.String()
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, errors.New("stt transcode: ffmpeg failed: " + err.Error() + ": " + msg)
	}
	if out.Len() == 0 {
		return nil, errors.New("stt transcode: ffmpeg produced no output")
	}
	if out.Len() >= len(wav) {
		// Compression that doesn't compress is a bug somewhere — forward the
		// original rather than a suspicious artifact.
		return nil, errors.New("stt transcode: output not smaller than input")
	}
	return out.Bytes(), nil
}

// replaceMultipartFilePart re-encodes the multipart body substituting the
// "file" part's payload/filename/content-type; every other part is copied
// verbatim (same contract as forceMultipartField: new boundary, caller updates
// the request Content-Type; (buf, boundary, false) unchanged on any failure).
func replaceMultipartFilePart(buf []byte, boundary, filename, contentType string, data []byte) ([]byte, string, bool) {
	mr := multipart.NewReader(bytes.NewReader(buf), boundary)
	var out bytes.Buffer
	mw := multipart.NewWriter(&out)
	replaced := false
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return buf, boundary, false
		}
		if part.FormName() == "file" {
			_ = part.Close()
			hdr := textproto.MIMEHeader{}
			hdr.Set("Content-Disposition",
				mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": filename}))
			hdr.Set("Content-Type", contentType)
			w, werr := mw.CreatePart(hdr)
			if werr != nil {
				return buf, boundary, false
			}
			if _, werr := w.Write(data); werr != nil {
				return buf, boundary, false
			}
			replaced = true
			continue
		}
		w, werr := mw.CreatePart(part.Header)
		if werr != nil {
			_ = part.Close()
			return buf, boundary, false
		}
		if _, cerr := io.Copy(w, part); cerr != nil {
			_ = part.Close()
			return buf, boundary, false
		}
		_ = part.Close()
	}
	if !replaced {
		return buf, boundary, false
	}
	if cerr := mw.Close(); cerr != nil {
		return buf, boundary, false
	}
	return out.Bytes(), mw.Boundary(), true
}

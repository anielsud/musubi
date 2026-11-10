package session

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/anielsud/musubi/internal/recorder/toolpath"
)

// SaveAudioMP3 encodes mono float32 samples as an MP3 at path, by way of a
// temporary WAV handed to ffmpeg (LAME VBR quality 6, roughly 115 kbit/s).
func SaveAudioMP3(samples []float32, sampleRate int, path string) error {
	if len(samples) == 0 {
		return fmt.Errorf("no audio to save")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("making audio directory: %w", err)
	}
	wavs, err := writeTempWAVs([][]float32{samples}, sampleRate)
	defer removeAll(wavs)
	if err != nil {
		return err
	}

	ffmpeg, err := toolpath.FFmpeg()
	if err != nil {
		return err
	}
	cmd := exec.Command(ffmpeg, "-y",
		"-i", wavs[0], "-codec:a", "libmp3lame", "-qscale:a", "6", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg MP3 encoding: %w: %s", err, out)
	}
	return nil
}

// SaveAudioM4A writes a session's audio as AAC in an MP4 container.
//
// With one track, the file holds just that track. With two or more,
// tracks[0] is the mic and tracks[1] the monitor (any further tracks are
// ignored, as in Tomoe), and the file holds three streams: 0 a mix of both,
// marked default so ordinary players play the whole meeting; 1 the mic;
// 2 the monitor. Diarization and replays read the separate streams (see
// DecodeTrackToFloat32), and Tomoe reads the same layout.
func SaveAudioM4A(tracks [][]float32, sampleRate int, path string) error {
	if len(tracks) == 0 {
		return fmt.Errorf("no audio tracks to save")
	}
	for i := range tracks {
		if len(tracks[i]) == 0 {
			return fmt.Errorf("audio track %d is empty", i)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("making audio directory: %w", err)
	}
	wavs, err := writeTempWAVs(tracks, sampleRate)
	defer removeAll(wavs)
	if err != nil {
		return err
	}

	args := []string{"-y"}
	for _, w := range wavs {
		args = append(args, "-i", w)
	}
	aac := []string{"-c:a", "aac", "-b:a", "128k"}
	if len(tracks) == 1 {
		args = append(args, "-map", "0:a")
		args = append(args, aac...)
	} else {
		args = append(args,
			"-filter_complex", "[0:a][1:a]amix=inputs=2:duration=longest[mixed]",
			"-map", "[mixed]", "-map", "0:a", "-map", "1:a")
		args = append(args, aac...)
		for i, title := range []string{"mixed", "mic", "monitor"} {
			args = append(args, "-metadata:s:a:"+strconv.Itoa(i), "title="+title)
		}
		for i, disp := range []string{"default", "0", "0"} {
			args = append(args, "-disposition:a:"+strconv.Itoa(i), disp)
		}
	}
	args = append(args, path)

	ffmpeg, err := toolpath.FFmpeg()
	if err != nil {
		return err
	}
	cmd := exec.Command(ffmpeg, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ffmpeg M4A encoding: %w: %s", err, out)
	}
	return nil
}

// writeTempWAVs writes each track to its own temporary WAV file and
// returns their paths. On an error the files made so far are still
// returned, for the caller to remove.
func writeTempWAVs(tracks [][]float32, sampleRate int) ([]string, error) {
	var paths []string
	for i, samples := range tracks {
		f, err := os.CreateTemp("", fmt.Sprintf("musubi-track%d-*.wav", i))
		if err != nil {
			return paths, fmt.Errorf("creating temporary WAV for track %d: %w", i, err)
		}
		paths = append(paths, f.Name())
		err = writeWAV(f, samples, sampleRate)
		_ = f.Close()
		if err != nil {
			return paths, fmt.Errorf("writing temporary WAV for track %d: %w", i, err)
		}
	}
	return paths, nil
}

func removeAll(paths []string) {
	for _, p := range paths {
		_ = os.Remove(p)
	}
}

// DecodeTrackToFloat32 decodes one audio stream of a file to 16 kHz mono
// float32 PCM with ffmpeg.
// track is 0-indexed. A dual-source session's M4A holds 0=mixed, 1=mic,
// 2=monitor; a single-source one holds just track 0 (see SaveAudioM4A).
func DecodeTrackToFloat32(path string, track int) ([]float32, error) {
	return decodePCM(path, "-map", "0:a:"+strconv.Itoa(track))
}

// decodePCM runs ffmpeg on path with the extra input options given, and
// converts its raw 16-bit output to float32 samples at pcmSampleRate.
func decodePCM(path string, extra ...string) ([]float32, error) {
	ffmpeg, err := toolpath.FFmpeg()
	if err != nil {
		return nil, err
	}
	args := append([]string{"-i", path}, extra...)
	args = append(args,
		"-ar", strconv.Itoa(pcmSampleRate), "-ac", "1",
		"-f", "s16le", "-acodec", "pcm_s16le", "-")
	raw, err := exec.Command(ffmpeg, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("decoding %s with ffmpeg: %w", path, err)
	}
	return pcm16ToFloat32(raw), nil
}

// pcm16ToFloat32 scales little-endian int16 samples into [-1, 1]. The
// divisor is 32767 (not 32768), matching Tomoe, so replays of the same
// file give the same samples in both apps.
func pcm16ToFloat32(raw []byte) []float32 {
	out := make([]float32, len(raw)/2)
	for i := range out {
		v := int16(binary.LittleEndian.Uint16(raw[2*i:]))
		out[i] = float32(v) / float32(math.MaxInt16)
	}
	return out
}

// IsM4A reports whether path names an M4A file, judged by its extension
// in any case.
func IsM4A(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".m4a")
}

// writeWAV writes float32 PCM samples as a 16-bit WAV file. The file is
// built in memory and written at once: one write per sample made saving a
// half-hour meeting take minutes (about 30 million system calls a track).
func writeWAV(f *os.File, samples []float32, sampleRate int) error {
	const (
		numChannels   = 1
		bitsPerSample = 16
	)
	blockAlign := numChannels * bitsPerSample / 8
	byteRate := sampleRate * blockAlign
	dataSize := blockAlign * len(samples)

	buf := make([]byte, 44+dataSize)
	le := binary.LittleEndian
	copy(buf[0:], "RIFF")
	le.PutUint32(buf[4:], uint32(36+dataSize))
	copy(buf[8:], "WAVE")
	copy(buf[12:], "fmt ")
	le.PutUint32(buf[16:], 16) // chunk size
	le.PutUint16(buf[20:], 1)  // PCM format
	le.PutUint16(buf[22:], uint16(numChannels))
	le.PutUint32(buf[24:], uint32(sampleRate))
	le.PutUint32(buf[28:], uint32(byteRate))
	le.PutUint16(buf[32:], uint16(blockAlign))
	le.PutUint16(buf[34:], uint16(bitsPerSample))
	copy(buf[36:], "data")
	le.PutUint32(buf[40:], uint32(dataSize))

	// Samples as 16-bit signed integers, clamped to [-1.0, 1.0].
	for i, sample := range samples {
		sample = max(-1, min(sample, 1))
		le.PutUint16(buf[44+2*i:], uint16(int16(math.Round(float64(sample)*32767.0))))
	}
	_, err := f.Write(buf)
	return err
}

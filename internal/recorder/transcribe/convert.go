package transcribe

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/anielsud/musubi/internal/recorder/toolpath"
)

// audioExts are the file types TranscribeFile takes. WAV is read directly;
// the rest go through ffmpeg first.
var audioExts = map[string]bool{".wav": true, ".flac": true, ".ogg": true, ".mp3": true, ".m4a": true}

func lowerExt(path string) string { return strings.ToLower(filepath.Ext(path)) }

// IsSupportedFormat reports whether TranscribeFile accepts path, judged by
// its extension.
func IsSupportedFormat(path string) bool { return audioExts[lowerExt(path)] }

// needsConversion reports whether path is a supported type other than WAV.
func needsConversion(path string) bool {
	ext := lowerExt(path)
	return audioExts[ext] && ext != ".wav"
}

// convertToWAV decodes srcPath with ffmpeg into a 16 kHz mono 16-bit WAV in
// the temp folder and returns its path; the caller deletes it.
func convertToWAV(srcPath string) (string, error) {
	ffmpeg, err := toolpath.FFmpeg()
	if err != nil {
		return "", fmt.Errorf("transcribing non-WAV files needs ffmpeg: %w", err)
	}

	out, err := os.CreateTemp("", "tomoe-*.wav")
	if err != nil {
		return "", fmt.Errorf("temp WAV for conversion: %w", err)
	}
	wavPath := out.Name()
	_ = out.Close()

	args := []string{"-i", srcPath, "-ar", "16000", "-ac", "1", "-sample_fmt", "s16", "-y", wavPath}
	cmd := exec.Command(ffmpeg, args...)
	if log, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(wavPath)
		return "", fmt.Errorf("ffmpeg couldn't convert %s: %w\n%s", srcPath, err, log)
	}
	return wavPath, nil
}

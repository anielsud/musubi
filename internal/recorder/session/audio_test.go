package session

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anielsud/musubi/internal/recorder/toolpath"
)

func needFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := toolpath.FFmpeg(); err != nil {
		t.Skip("ffmpeg not installed")
	}
}

func tone(freq float64, amp float32, n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = amp * float32(math.Sin(2*math.Pi*freq*float64(i)/pcmSampleRate))
	}
	return out
}

func rms(x []float32) float64 {
	var sum float64
	for _, v := range x {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(max(len(x), 1)))
}

func TestSaveAudioRejectsEmptyInput(t *testing.T) {
	dir := t.TempDir()
	if err := SaveAudioMP3(nil, 16000, filepath.Join(dir, "a.mp3")); err == nil {
		t.Error("MP3 with no samples saved")
	}
	if err := SaveAudioM4A(nil, 16000, filepath.Join(dir, "a.m4a")); err == nil {
		t.Error("M4A with no tracks saved")
	}
	if err := SaveAudioM4A([][]float32{{0.1}, nil}, 16000, filepath.Join(dir, "b.m4a")); err == nil {
		t.Error("M4A with an empty track saved")
	}
}

func TestSaveAudioMP3DecodesBack(t *testing.T) {
	needFFmpeg(t)
	path := filepath.Join(t.TempDir(), "sub", "a.mp3") // directory made on the way
	if err := SaveAudioMP3(tone(440, 0.5, pcmSampleRate), pcmSampleRate, path); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeToFloat32(path)
	if err != nil {
		t.Fatal(err)
	}
	// MP3 pads, so allow some slack on length.
	if len(got) < pcmSampleRate*9/10 || rms(got) < 0.2 {
		t.Errorf("decoded %d samples, rms %.3f", len(got), rms(got))
	}
}

// A dual-source file must hold mixed, mic, monitor as streams 0, 1, 2: that
// is what Tomoe and diarization read.
func TestSaveAudioM4ATrackLayout(t *testing.T) {
	needFFmpeg(t)
	n := pcmSampleRate / 2
	mic := tone(300, 0.6, n)
	monitor := make([]float32, n) // silent
	path := filepath.Join(t.TempDir(), "audio.m4a")
	if err := SaveAudioM4A([][]float32{mic, monitor}, pcmSampleRate, path); err != nil {
		t.Fatal(err)
	}
	level := make([]float64, 3)
	for track := range level {
		pcm, err := DecodeTrackToFloat32(path, track)
		if err != nil {
			t.Fatalf("track %d: %v", track, err)
		}
		level[track] = rms(pcm)
	}
	if level[1] < 0.2 || level[2] > 0.01 || level[0] < 0.05 {
		t.Errorf("track levels (mixed, mic, monitor) = %.3f", level)
	}
	if _, err := DecodeTrackToFloat32(path, 3); err == nil {
		t.Error("a fourth stream decoded")
	}

	single := filepath.Join(t.TempDir(), "one.m4a")
	if err := SaveAudioM4A([][]float32{mic}, pcmSampleRate, single); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTrackToFloat32(single, 1); err == nil {
		t.Error("single-source file has a second stream")
	}
}

func TestIsM4A(t *testing.T) {
	for path, want := range map[string]bool{
		"/s/audio.m4a": true, "AUDIO.M4A": true, "a.mp3": false, "m4a": false, "/s.m4a/audio": false,
	} {
		if got := IsM4A(path); got != want {
			t.Errorf("IsM4A(%q) = %v", path, got)
		}
	}
}

func TestPCM16ToFloat32(t *testing.T) {
	raw := []byte{0xff, 0x7f, 0x01, 0x80, 0, 0, 0x01} // 32767, -32767, 0, odd byte dropped
	got := pcm16ToFloat32(raw)
	if len(got) != 3 || got[0] != 1 || got[1] != -1 || got[2] != 0 {
		t.Errorf("got %v", got)
	}
}

func TestWriteWAVHeader(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "*.wav")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWAV(f, []float32{0, 0.5, -0.5}, 8000); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	data, _ := os.ReadFile(f.Name())
	le := binary.LittleEndian
	if len(data) != 44+6 || string(data[:4]) != "RIFF" || string(data[8:16]) != "WAVEfmt " ||
		le.Uint32(data[24:]) != 8000 || le.Uint32(data[28:]) != 16000 || le.Uint32(data[40:]) != 6 {
		t.Errorf("header % x", data[:44])
	}
}

// The WAV is byte for byte what the previous per-sample writer produced
// (header, rounding and clamping), and a half-hour track saves in well
// under a second instead of minutes.
func TestWriteWAVSameBytesFast(t *testing.T) {
	samples := []float32{0, 0.5, 1.0, 1.5, -0.25, -1.0, -3, 0.33333, -0.000001, 0.99999}
	var want bytes.Buffer
	le := binary.LittleEndian
	want.WriteString("RIFF")
	_ = binary.Write(&want, le, uint32(36+len(samples)*2))
	want.WriteString("WAVEfmt ")
	_ = binary.Write(&want, le, uint32(16))
	_ = binary.Write(&want, le, uint16(1))
	_ = binary.Write(&want, le, uint16(1))
	_ = binary.Write(&want, le, uint32(16000))
	_ = binary.Write(&want, le, uint32(32000))
	_ = binary.Write(&want, le, uint16(2))
	_ = binary.Write(&want, le, uint16(16))
	want.WriteString("data")
	_ = binary.Write(&want, le, uint32(len(samples)*2))
	for _, s := range samples {
		s = float32(math.Max(-1, math.Min(1, float64(s))))
		_ = binary.Write(&want, le, int16(math.Round(float64(s)*32767.0)))
	}
	f, err := os.CreateTemp(t.TempDir(), "same-*.wav")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeWAV(f, samples, 16000); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	got, _ := os.ReadFile(f.Name())
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("bytes differ:\n got %x\nwant %x", got, want.Bytes())
	}

	long := make([]float32, 30*60*16000)
	for i := range long {
		long[i] = float32(math.Sin(float64(i) / 20))
	}
	f2, err := os.CreateTemp(t.TempDir(), "long-*.wav")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if err := writeWAV(f2, long, 16000); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Errorf("a half-hour track took %s", d)
	}
	_ = f2.Close()
}

package gui

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/anielsud/musubi/internal/store"
)

func TestVisibleNotes(t *testing.T) {
	got := visibleNotes([]store.Hit{
		{Text: "You: shall we?\nAna Example: yes"},
		{Text: "You: [your notes] first line\nYou: [your notes] second line"},
	})
	if got != "first line\nsecond line" {
		t.Errorf("%q", got)
	}
}

func TestThumbnail(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 1200, 600))
	for y := 0; y < 600; y++ {
		for x := 0; x < 1200; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 80, 255})
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, nil)
	th, err := thumbnail(b.Bytes(), 320)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(th))
	if err != nil || cfg.Width != 320 || cfg.Height != 160 {
		t.Errorf("%+v %v", cfg, err)
	}
	if _, err := thumbnail([]byte("not a jpeg"), 320); err == nil {
		t.Error("not a JPEG scaled")
	}
}

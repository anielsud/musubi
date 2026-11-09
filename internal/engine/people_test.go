package engine

import (
	"image"
	"image/color"
	"testing"
)

func TestSpeakerName(t *testing.T) {
	for in, want := range map[string]string{
		"Person 3 (Kai Moreno)":  "kai moreno",
		"Speaker 1 (Robin Vale)": "robin vale",
		"Kai Moreno (Guest)":     "kai moreno",
		"Vale, Robin":            "vale robin",
		"Person 2":               "person 2",
	} {
		if got := speakerName(in); got != want {
			t.Errorf("speakerName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A camera-off tile is a picture on a plain background; video varies at
// the edges. centre crops the middle square.
func TestCameraOn(t *testing.T) {
	off := image.NewRGBA(image.Rect(0, 0, 160, 90))
	for y := 0; y < 90; y++ {
		for x := 0; x < 160; x++ {
			off.Set(x, y, color.RGBA{40, 40, 44, 255}) // plain background
		}
	}
	for y := 30; y < 60; y++ {
		for x := 65; x < 95; x++ {
			off.Set(x, y, color.RGBA{200, 120, 60, 255}) // the picture in the middle
		}
	}
	if cameraOn(off) {
		t.Error("camera-off tile read as video")
	}
	on := image.NewRGBA(image.Rect(0, 0, 160, 90))
	for y := 0; y < 90; y++ {
		for x := 0; x < 160; x++ {
			v := uint8((x*37 + y*91) % 256) // a busy room
			on.Set(x, y, color.RGBA{v, v / 2, 255 - v, 255})
		}
	}
	if !cameraOn(on) {
		t.Error("video tile read as camera-off")
	}
	if b := centre(off, 0.5).Bounds(); b.Dx() < 44 || b.Dx() > 45 || b.Dx() != b.Dy() {
		t.Errorf("centre crop %v", b)
	}
}

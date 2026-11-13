package main

import (
	"encoding/gob"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/anielsud/musubi/internal/recorder/diarize"
)

// loadFingerprints reads a session's saved diarization fingerprints
// (diarization.gob) and how they were taken (diarization.json).
func loadFingerprints(dir string) (*diarize.Prepared, diarize.StreamInfo, error) {
	var info diarize.StreamInfo
	f, err := os.Open(filepath.Join(dir, "diarization.gob"))
	if err != nil {
		return nil, info, err
	}
	defer f.Close()
	var p diarize.Prepared
	if err := gob.NewDecoder(f).Decode(&p); err != nil {
		return nil, info, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "diarization.json"))
	if err != nil || json.Unmarshal(b, &info) != nil {
		info = diarize.StreamInfo{Stride: 1, Params: diarize.DefaultParams()}
	}
	if info.Stride < 1 {
		info.Stride = 1
	}
	return &p, info, nil
}

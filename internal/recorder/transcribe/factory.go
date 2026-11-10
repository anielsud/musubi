package transcribe

import (
	"fmt"

	"github.com/anielsud/musubi/internal/recorder/models"
)

// NewEngineSetFromConfig builds the English engine the model setting picks
// (cfg.Model; Cohere Transcribe for "auto") when it is downloaded, else the
// multilingual Parakeet at cfg's paths, and returns it as a one-language
// set.
func NewEngineSetFromConfig(cfg Config, status *models.Status) (*EngineSet, error) {
	defaultLang := "en"

	// The model for the default language, as the setting picks it, if
	// downloaded (else the multilingual Parakeet).
	asr := models.ASRModels[0]
	if status != nil {
		var fellBack bool
		asr, fellBack = status.ASRModelFor(cfg.Model, defaultLang)
		if asr.Kind == models.ASRKindTransducer && asr.ID != models.ASRModels[0].ID {
			cfg.EncoderPath, cfg.DecoderPath, cfg.JoinerPath, cfg.TokensPath = status.ASRModelFiles(asr)
		}
		if fellBack {
			fmt.Printf("Transcription model %s isn't downloaded; using %s\n", models.ResolveASRModel(cfg.Model, defaultLang).Name, asr.Name)
		}
	}

	var parakeet Engine
	var err error
	if asr.Kind == models.ASRKindCohere {
		parakeet, err = NewCohereEngine(status.ASRModelDir(asr), defaultLang, cfg.NumThreads, cfg.VADPath)
	} else {
		parakeet, err = NewEngine(cfg)
	}
	if err != nil {
		return nil, err
	}
	set, err := NewEngineSet(map[string]Engine{defaultLang: parakeet}, defaultLang)
	if err != nil {
		parakeet.Close()
		return nil, err
	}
	return set, nil
}

package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/anielsud/musubi/internal/recorder/config"
	"github.com/anielsud/musubi/internal/recorder/models"
	"github.com/anielsud/musubi/internal/recorder/session"
)

// diarizeCmd is the post-meeting speaker pass, run by `run` as a child
// process (session.RunDiarizeWithRetry): sherpa-onnx can segfault inside
// diarization, and in a child that loses one attempt instead of the
// recorder and whatever it is recording.
var diarizeCmd = &cobra.Command{
	Use:    "diarize <session-id>",
	Short:  "(internal) Diarize a saved recording in a child process",
	Hidden: true,
	Args:   cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		data, _ := cmd.Flags().GetString("data")
		if _, err := useDataDir(data); err != nil {
			return err
		}
		return diarizeSession(args[0])
	},
}

func init() {
	// The recorder runs the models on the CPU only, so --cpu changes
	// nothing; it is accepted because the retry ladder passes it.
	diarizeCmd.Flags().Bool("cpu", false, "run on the CPU (the recorder always does)")
	diarizeCmd.Flags().String("data", defaultDataDir(), "the recorder's data folder holding the session (never Tomoe's)")
	rootCmd.AddCommand(diarizeCmd)
}

// diarizeSession relabels a saved session's speakers with sherpa-onnx's
// diarization and saves it again, in the folder config.DataDir names.
func diarizeSession(id string) error {
	cfg, err := loadConfig(config.MusubiPath())
	if err != nil {
		return err
	}
	status := models.NewManagerIn(config.ModelDirs(cfg)).WithLiveModel(cfg.Transcription.LiveModel).Check()
	if !status.DiarizationReady() {
		return fmt.Errorf("the diarization models aren't in %s", strings.Join(config.ModelDirs(cfg), " or "))
	}
	sessions := session.NewStore(config.SessionDir())
	sess, err := sessions.Load(id)
	if err != nil {
		return err
	}

	sm, smPath, _ := status.SpeakerModelFor(cfg.Meeting.SpeakerModel, sess.Language)
	fmt.Printf("diarize %s: speaker model %s\n", id, sm.ID)
	relabeled, err := session.ReidentifyByDiarization(sess, session.DiarizeConfig{
		SegmentationModelPath: status.SpeakerSegmentationPath,
		EmbeddingModelPath:    smPath,
		Threshold:             float32(sm.DiarizeThreshold),
		MergeThreshold:        sm.DiarizeMerge,
		SplitOnSpeakerChange:  cfg.Meeting.SplitOnSpeakerChange,
	})
	switch {
	case err != nil:
		return fmt.Errorf("diarizing %s: %w", id, err)
	case relabeled == 0:
		fmt.Printf("diarize %s: nothing to relabel\n", id)
		return nil
	}
	if err := sessions.Save(sess); err != nil {
		return fmt.Errorf("saving %s: %w", id, err)
	}
	fmt.Printf("diarize %s: %d lines relabeled\n", id, relabeled)
	return nil
}

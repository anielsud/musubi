//go:build !darwin

package recording

// Off macOS there are no permissions to ask for and no installer to run.

func ffmpegFix() *ToolFix {
	return commandFix("Copy install command", "sudo apt install ffmpeg")
}

func permissionStatuses() []ToolStatus { return nil }

func platformFix(*Recorder, string) func() (string, error) { return nil }

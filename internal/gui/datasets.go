package gui

import (
	"os"
	"path/filepath"

	"github.com/anielsud/musubi/internal/config"
)

// lastDatasetPath remembers the dataset last opened, for the next start.
func lastDatasetPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "musubi", "gui-last-dataset")
}

// startConfig is the config this window opens: MUSUBI_CONFIG when set (a
// window opened for a dataset), else the dataset last opened, else the
// first available dataset, else the default config.
func startConfig() string {
	if p := os.Getenv("MUSUBI_CONFIG"); p != "" {
		return p
	}
	ds, _ := config.LoadDatasets()
	if b, err := os.ReadFile(lastDatasetPath()); err == nil {
		for _, d := range ds {
			if d.Key == string(b) && d.Available() {
				return d.Config
			}
		}
	}
	for _, d := range ds {
		if d.Available() {
			return d.Config
		}
	}
	return config.Path()
}

// datasetName names the dataset a config belongs to, for titles.
func datasetName(path string) string {
	ds, _ := config.LoadDatasets()
	for _, d := range ds {
		if d.Config == path {
			return d.Name
		}
	}
	if path == config.Path() {
		return "default"
	}
	return filepath.Base(filepath.Dir(path))
}

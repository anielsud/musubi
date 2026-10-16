package gui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/anielsud/musubi/internal/config"
)

// DatasetView is a dataset as the dropdown shows it.
type DatasetView struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Note      string `json:"note,omitempty"`
	Available bool   `json:"available"`
	Current   bool   `json:"current"`
}

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

// Datasets lists the datasets in the datasets file.
func (a *App) Datasets() ([]DatasetView, error) {
	ds, err := config.LoadDatasets()
	if err != nil {
		return nil, err
	}
	out := make([]DatasetView, 0, len(ds))
	for _, d := range ds {
		out = append(out, DatasetView{Key: d.Key, Name: d.Name, Note: d.Note, Available: d.Available(), Current: d.Config == a.cfgPath})
	}
	return out, nil
}

func findDataset(key string) (config.Dataset, error) {
	ds, err := config.LoadDatasets()
	if err != nil {
		return config.Dataset{}, err
	}
	for _, d := range ds {
		if d.Key == key {
			if !d.Available() {
				return d, fmt.Errorf("dataset %q is not built yet", d.Name)
			}
			return d, nil
		}
	}
	return config.Dataset{}, fmt.Errorf("no dataset %q", key)
}

// SwitchDataset opens another dataset in this window; the frontend reloads.
func (a *App) SwitchDataset(key string) error {
	d, err := findDataset(key)
	if err != nil {
		return err
	}
	if err := a.openConfig(d.Config); err != nil {
		a.err = err.Error()
		return err
	}
	os.WriteFile(lastDatasetPath(), []byte(d.Key), 0o600)
	a.emit("app:status", a.Status())
	return nil
}

// OpenDatasetWindow opens a dataset in a window of its own (another app
// process), for comparing two side by side.
func (a *App) OpenDatasetWindow(key string) error {
	d, err := findDataset(key)
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), "MUSUBI_CONFIG="+d.Config)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

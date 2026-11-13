package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// Dataset is one instance the app can open: a config of its own, so its own
// data directory. The datasets file lists them for the app's dropdown, so
// experiment instances (a reference, the best setup so far, a candidate)
// can be opened side by side.
type Dataset struct {
	Key    string `toml:"key"`
	Name   string `toml:"name"`
	Config string `toml:"config"`
	// Note says what is in it, or why it isn't built yet.
	Note string `toml:"note"`
}

// Available reports whether the dataset's config exists.
func (d Dataset) Available() bool {
	if d.Config == "" {
		return false
	}
	_, err := os.Stat(d.Config)
	return err == nil
}

type datasetsFile struct {
	Datasets []Dataset `toml:"dataset"`
}

// DatasetsPath is the datasets file: MUSUBI_DATASETS, else
// ~/.config/musubi/datasets.toml.
func DatasetsPath() string {
	if p := os.Getenv("MUSUBI_DATASETS"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "musubi", "datasets.toml")
}

// LoadDatasets reads the datasets file; a missing file is no datasets.
func LoadDatasets() ([]Dataset, error) {
	b, err := os.ReadFile(DatasetsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f datasetsFile
	if err := toml.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Datasets, nil
}

// DatasetName names the dataset a config belongs to, for titles and its
// backups' names: its name in the datasets file, "default" for the default
// config, else its folder's name.
func DatasetName(path string) string {
	ds, _ := LoadDatasets()
	for _, d := range ds {
		if d.Config == path {
			return d.Name
		}
	}
	if path == Path() {
		return "default"
	}
	return filepath.Base(filepath.Dir(path))
}

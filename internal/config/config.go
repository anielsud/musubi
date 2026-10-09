// Package config loads Musubi's TOML config over DefaultConfig, so a key
// missing from the file keeps its default.
package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	// DataDir holds the database, raw blobs and inbox.
	DataDir string `toml:"data_dir"`
	// TomoeSessions is Tomoe's session directory; empty disables the source.
	TomoeSessions string `toml:"tomoe_sessions"`

	Owner       Owner       `toml:"owner"`
	Embed       Embed       `toml:"embed"`
	Sensitivity Sensitivity `toml:"sensitivity"`
}

type Sensitivity struct {
	// InternalMinAudience is the audience size from which an all-internal
	// meeting, mail or chat is labelled internal instead of confidential.
	// The default (15) means only broadcasts are internal; 3 restores the
	// first behaviour, where any internal group of three was internal.
	InternalMinAudience int `toml:"internal_min_audience"`
}

// Owner identifies whose knowledge base this is. "You" in a Tomoe
// transcript resolves to this person, and they are in every item's audience.
type Owner struct {
	Email string `toml:"email"`
	Name  string `toml:"name"`
	// InternalDomains are email domains treated as inside the company.
	InternalDomains []string `toml:"internal_domains"`
}

type Embed struct {
	// Provider is "ollama", "hash" (deterministic, for tests) or "none".
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	URL      string `toml:"url"`
}

func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		DataDir:       filepath.Join(home, ".local", "share", "musubi"),
		TomoeSessions: filepath.Join(home, ".local", "share", "tomoe", "sessions"),
		Embed:         Embed{Provider: "ollama", Model: "nomic-embed-text", URL: "http://127.0.0.1:11434"},
		Sensitivity:   Sensitivity{InternalMinAudience: 15},
	}
}

// Path is the default config location.
func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "musubi", "config.toml")
}

// Load reads path over the defaults. A missing file is not an error.
func Load(path string) (Config, error) {
	c := DefaultConfig()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := toml.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, nil
}

func (c Config) DBPath() string   { return filepath.Join(c.DataDir, "musubi.db") }
func (c Config) RawDir() string   { return filepath.Join(c.DataDir, "raw") }
func (c Config) InboxDir() string { return filepath.Join(c.DataDir, "inbox") }
func (c Config) Registry() string { return filepath.Join(c.DataDir, "registry.toml") }

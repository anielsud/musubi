package transcribe

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// EngineSet maps language codes to engines. It isn't an Engine itself:
// callers choose a language and decode with what Get returns.
type EngineSet struct {
	engines     map[string]Engine
	defaultLang string
}

// NewEngineSet wraps engines, which must include one for defaultLang
// ("" meaning "en").
func NewEngineSet(engines map[string]Engine, defaultLang string) (*EngineSet, error) {
	if len(engines) == 0 {
		return nil, errors.New("an engine set needs at least one engine")
	}
	if defaultLang == "" {
		defaultLang = "en"
	}
	if _, ok := engines[defaultLang]; !ok {
		return nil, fmt.Errorf("no engine for the default language %q", defaultLang)
	}
	return &EngineSet{engines: engines, defaultLang: defaultLang}, nil
}

// Get is lang's engine, or the default language's when lang has none.
func (es *EngineSet) Get(lang string) Engine {
	if e, ok := es.engines[lang]; ok {
		return e
	}
	return es.Default()
}

// Default is the default language's engine.
func (es *EngineSet) Default() Engine { return es.engines[es.defaultLang] }

// DefaultLang is the language Get falls back to.
func (es *EngineSet) DefaultLang() string { return es.defaultLang }

// Languages lists the languages with an engine, in sorted order.
func (es *EngineSet) Languages() []string { return slices.Sorted(maps.Keys(es.engines)) }

// Close closes every engine in the set.
func (es *EngineSet) Close() {
	for _, e := range es.engines {
		e.Close()
	}
}

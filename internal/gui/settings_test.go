package gui

import (
	"testing"

	"github.com/anielsud/musubi/internal/config"
)

// Every setting offered in Settings exists, with the type it is shown as.
func TestPrefFieldsExist(t *testing.T) {
	c := config.DefaultConfig()
	for tab, gs := range prefTabs {
		for _, g := range gs {
			for _, f := range g.Fields {
				v := c.Value(f.Section, f.Key)
				ok := false
				switch f.Type {
				case "bool":
					_, ok = v.(bool)
				case "int":
					_, ok = v.(int)
				case "string":
					_, ok = v.(string)
				}
				if !ok {
					t.Errorf("%s: %s.%s is %T, shown as %s", tab, f.Section, f.Key, v, f.Type)
				}
			}
		}
	}
}

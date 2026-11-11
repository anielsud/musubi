package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Edit sets (or, with Delete, removes) one key of one table in a config
// file. Section is the table's dotted name ("knowledge.threads.model");
// Delete with no Key removes the whole table.
type Edit struct {
	Section, Key string
	// Value is a string, bool, int, float64 or map[string]string (written
	// as an inline table).
	Value  any
	Delete bool
}

var (
	headerRe = regexp.MustCompile(`^\s*\[([^\[\]]+)\]\s*(#.*)?$`)
	keyRe    = regexp.MustCompile(`^\s*("[^"]*"|[A-Za-z0-9_-]+)\s*=`)
)

// EditFile applies edits to the config at path in place: only the lines of
// the keys it sets change, so the owner's comments, order and other keys
// stay as they were. A key is added at the end of its table, a table at the
// end of the file. The result must still load, or nothing is written; the
// file before the change is kept beside it as <name>.bak.
func EditFile(path string, edits []Edit) error {
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	out, err := EditText(string(b), edits)
	if err != nil {
		return err
	}
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(out), 0o600); err != nil {
		return err
	}
	if _, err := Load(tmp); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("config edit would not load: %w", err)
	}
	if len(b) > 0 {
		if err := os.WriteFile(path+".bak", b, 0o600); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	return os.Rename(tmp, filepath.Clean(path))
}

// EditText is EditFile on the file's text.
func EditText(text string, edits []Edit) (string, error) {
	lines := strings.Split(text, "\n")
	for _, e := range edits {
		var err error
		if lines, err = editLines(lines, e); err != nil {
			return "", err
		}
	}
	return strings.Join(lines, "\n"), nil
}

func editLines(lines []string, e Edit) ([]string, error) {
	val := ""
	if !e.Delete {
		v, err := tomlValue(e.Value)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", e.Section, e.Key, err)
		}
		val = v
	}
	// The table's lines: after its header, up to the next header. Keys
	// before any header belong to the root table, Section "".
	start, end := -1, len(lines)
	for i, l := range lines {
		m := headerRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		if e.Section == "" || start >= 0 {
			end = i
			break
		}
		if strings.TrimSpace(m[1]) == e.Section {
			start = i
		}
	}
	if e.Section != "" && start < 0 {
		if e.Delete {
			return lines, nil
		}
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		return append(lines, "", "["+e.Section+"]", e.Key+" = "+val, ""), nil
	}
	from := start + 1 // 0 for the root table
	if e.Key == "" {
		if !e.Delete || e.Section == "" {
			return nil, fmt.Errorf("an edit needs a key")
		}
		// The header and its keys go; a comment just above the next header
		// is that table's, and stays.
		stop := start + 1
		for i := start + 1; i < end; i++ {
			if keyRe.MatchString(lines[i]) {
				stop = i + 1
			}
		}
		if stop < len(lines) && strings.TrimSpace(lines[stop]) == "" && start > 0 && strings.TrimSpace(lines[start-1]) == "" {
			stop++ // no double blank line where it was
		}
		return append(lines[:start:start], lines[stop:]...), nil
	}
	last := -1 // the table's last key line
	for i := from; i < end; i++ {
		m := keyRe.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		last = i
		if strings.Trim(m[1], `"`) != e.Key {
			continue
		}
		if strings.Contains(lines[i], "'''") || strings.Contains(lines[i], `"""`) || openArray(lines[i]) {
			return nil, fmt.Errorf("%s.%s spans several lines; edit it by hand", e.Section, e.Key)
		}
		if e.Delete {
			return append(lines[:i:i], lines[i+1:]...), nil
		}
		indent := lines[i][:len(lines[i])-len(strings.TrimLeft(lines[i], " \t"))]
		line := indent + m[1] + " = " + val
		if c := trailingComment(lines[i]); c != "" {
			line += "  " + c
		}
		lines[i] = line
		return lines, nil
	}
	if e.Delete {
		return lines, nil
	}
	at := from
	if last >= 0 {
		at = last + 1
	}
	out := append(append(append([]string{}, lines[:at]...), e.Key+" = "+val), lines[at:]...)
	return out, nil
}

// trailingComment is the "# …" after a key's value, outside any string.
func trailingComment(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return strings.TrimSpace(line[i:])
		}
	}
	return ""
}

// openArray: a value that opens an array and doesn't close it on the line.
func openArray(line string) bool {
	v := line[strings.Index(line, "=")+1:]
	if c := trailingComment(v); c != "" {
		v = v[:strings.Index(v, c)]
	}
	return strings.Count(v, "[") > strings.Count(v, "]")
}

func tomlValue(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return tomlString(x), nil
	case bool:
		return strconv.FormatBool(x), nil
	case int:
		return strconv.Itoa(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		s := strconv.FormatFloat(x, 'f', -1, 64)
		if !strings.ContainsAny(s, ".eE") {
			s += ".0"
		}
		return s, nil
	case map[string]string:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = tomlString(k) + " = " + tomlString(x[k])
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	}
	return "", fmt.Errorf("can't write a %T", v)
}

// tomlString writes a basic string as the configs do, or a literal one
// when that saves escapes (JSON in a header).
func tomlString(s string) string {
	if !strings.ContainsAny(s, "\"\\\n\r\t") {
		return `"` + s + `"`
	}
	if !strings.ContainsAny(s, "'\n\r") {
		return "'" + s + "'"
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

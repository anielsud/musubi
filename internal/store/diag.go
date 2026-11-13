package store

// Counts and versions for a diagnostics report (internal/diagnostics):
// numbers only, no content.

// Versions are the database's schema and format, and the newest this
// build reads.
type Versions struct {
	Schema, Format           int
	BuildSchema, BuildFormat int
}

func (s *Store) Versions() (Versions, error) {
	v := Versions{BuildSchema: schemaVersion, BuildFormat: formatVersion}
	if err := s.rdb.QueryRow(`PRAGMA user_version`).Scan(&v.Schema); err != nil {
		return v, err
	}
	f, err := storedFormat(s.rdb)
	v.Format = f
	return v, err
}

// ItemCount is how many items one source holds of one kind.
type ItemCount struct {
	Source, Kind string
	N            int
}

// ItemCounts counts items by source and kind.
func (s *Store) ItemCounts() ([]ItemCount, error) {
	rows, err := s.rdb.Query(`SELECT source, kind, count(*) FROM items GROUP BY 1, 2 ORDER BY 1, 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ItemCount
	for rows.Next() {
		var c ItemCount
		if err := rows.Scan(&c.Source, &c.Kind, &c.N); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ThreadCounts counts threads and the statements placed in them.
func (s *Store) ThreadCounts() (threads, placed int, err error) {
	if err = s.rdb.QueryRow(`SELECT count(*) FROM topics`).Scan(&threads); err != nil {
		return
	}
	err = s.rdb.QueryRow(`SELECT count(*) FROM thread_items`).Scan(&placed)
	return
}

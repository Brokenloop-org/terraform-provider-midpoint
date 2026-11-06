package mpschema

import (
	"compress/gzip"
	"embed"
	"encoding/json"
	"sync"
)

//go:embed tables/*.json.gz
var tableFiles embed.FS

// tables holds a loader per supported line. A line's table is read once, on
// first use, and is shared: callers must not change it.
var tables = map[string]func() *Table{
	"4.9":  tableLoader("4.9"),
	"4.10": tableLoader("4.10"),
	"4.11": tableLoader("4.11"),
}

func tableLoader(line string) func() *Table {
	return sync.OnceValue(func() *Table {
		t, err := loadTable(line)
		if err != nil {
			// Unreachable for the tables TestFor_allLines reads. Having no table
			// skips the check, which is better than failing a plan over it.
			return nil
		}
		return t
	})
}

func loadTable(line string) (*Table, error) {
	f, err := tableFiles.Open("tables/" + line + ".json.gz")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	var t Table
	if err := json.NewDecoder(zr).Decode(&t); err != nil {
		return nil, err
	}
	return &t, nil
}

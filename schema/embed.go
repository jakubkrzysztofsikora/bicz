// Package schema exposes the frozen, forward-only SQL migrations in this
// directory to the migration runner (see docs/SCHEMA.md "Migration policy").
//
// The migrations are embedded so that the binary is self-contained: `bicz init`
// never depends on the working directory or on a separate install step.
package schema

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed *.sql
var files embed.FS

// Migration is one numbered, forward-only migration file.
//
// Version is the numeric prefix NN of NN_name.sql; Name is the file name as
// written on disk; SQL is the file body, executed verbatim inside one
// transaction.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Migrations returns every migration in ascending version order.
//
// A file that is not named NNN_name.sql, or two files claiming the same
// version, is a hard error: the runner never guesses at migration order.
func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("schema: read embedded migrations: %w", err)
	}
	out := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("schema: unexpected directory %q in migration set", entry.Name())
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".sql") {
			return nil, fmt.Errorf("schema: unexpected non-SQL file %q in migration set", name)
		}
		digits, _, ok := strings.Cut(name, "_")
		if !ok || digits == "" {
			return nil, fmt.Errorf("schema: migration %q is not named NNN_name.sql", name)
		}
		for i := range len(digits) {
			if digits[i] < '0' || digits[i] > '9' {
				return nil, fmt.Errorf("schema: migration %q has a non-numeric version prefix", name)
			}
		}
		version, err := strconv.Atoi(digits)
		if err != nil || version <= 0 {
			return nil, fmt.Errorf("schema: migration %q has an invalid version prefix", name)
		}
		body, err := files.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("schema: read %s: %w", name, err)
		}
		out = append(out, Migration{Version: version, Name: name, SQL: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	for i := 1; i < len(out); i++ {
		if out[i].Version <= out[i-1].Version {
			return nil, fmt.Errorf("schema: migration version %d claimed by both %s and %s",
				out[i].Version, out[i-1].Name, out[i].Name)
		}
	}
	return out, nil
}

// Highest returns the highest known migration version, or 0 when the embedded
// set is empty.
func Highest() (int, error) {
	migrations, err := Migrations()
	if err != nil {
		return 0, err
	}
	if len(migrations) == 0 {
		return 0, nil
	}
	return migrations[len(migrations)-1].Version, nil
}

// Command midpoint-convert turns a directory tree of midPoint object files
// (JSON, YAML or XML) into Terraform configuration for the midpoint provider.
// It reads files only and never contacts a server.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/jplana/terraform-provider-midpoint/internal/convert"
)

const usage = `Usage: midpoint-convert [--format hcl|json] [--split-files] --out <dir> <input-dir>...

Converts midPoint object files below the input directories to Terraform
configuration and writes it, with report.md, to <dir>.

  --format hcl|json  output syntax: hcl (default) or Terraform JSON (.tf.json)
  --split-files      move large resource attributes to YAML and XSD files
  --out <dir>        output directory; created if missing, refused if it
                     exists and is not empty

Exit codes: 0 ok, 1 usage or I/O error, 2 two files define the same oid
differently.
`

func main() { os.Exit(cli(os.Args[1:], os.Stderr)) }

func cli(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("midpoint-convert", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	format := fs.String("format", "hcl", "output format, hcl or json")
	split := fs.Bool("split-files", false, "move large attributes to side files")
	out := fs.String("out", "", "output directory")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *out == "" || fs.NArg() == 0 {
		fs.Usage()
		return 1
	}
	if err := checkOut(*out); err != nil {
		fmt.Fprintln(stderr, "midpoint-convert:", err)
		return 1
	}

	files, report, err := convert.Convert(fs.Args(), convert.Options{Format: *format, SplitFiles: *split, OutDir: *out})
	if err != nil {
		fmt.Fprintln(stderr, "midpoint-convert:", err)
		var conflict *convert.ConflictError
		if errors.As(err, &conflict) {
			return 2
		}
		return 1
	}
	files["report.md"] = report.Markdown()
	if err := writeFiles(*out, files); err != nil {
		fmt.Fprintln(stderr, "midpoint-convert:", err)
		return 1
	}
	return 0
}

// writeFiles writes files below out in sorted order. It writes into a
// temporary sibling directory and renames it to out, so a failure leaves out
// as it was.
func writeFiles(out string, files map[string][]byte) error {
	parent := filepath.Dir(filepath.Clean(out))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".midpoint-convert-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp) // a no-op after the rename
	if err := os.Chmod(tmp, 0o755); err != nil {
		return err
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, files[name], 0o644); err != nil {
			return err
		}
	}
	if err := os.Remove(out); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err // checkOut guarantees an existing out is empty
	}
	return os.Rename(tmp, out)
}

// checkOut refuses an existing output directory that is not empty, so a run
// never overwrites or mixes with files it did not write.
func checkOut(dir string) error {
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case len(entries) > 0:
		return fmt.Errorf("output directory %s is not empty; choose a new or empty directory", dir)
	}
	return nil
}

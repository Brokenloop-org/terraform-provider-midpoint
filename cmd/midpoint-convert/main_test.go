package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "midpoint-convert-test")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "midpoint-convert")
	if out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		os.Stderr.Write(out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// run executes the binary and returns its exit code and stderr.
func run(t *testing.T, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, &exit):
		return exit.ExitCode(), stderr.String()
	default:
		t.Fatal(err)
		return -1, ""
	}
}

func TestMain_convertsFixtureTree(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out", "nested")
	code, stderr := run(t, "--out", out, "../../internal/convert/testdata/catalogue")
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	golden := "../../internal/convert/testdata/golden"
	want := map[string]bool{}
	err := filepath.WalkDir(golden, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(golden, p)
		want[rel] = true
		wantBytes, _ := os.ReadFile(p)
		got, err := os.ReadFile(filepath.Join(out, rel))
		if err != nil {
			t.Errorf("missing %s: %v", rel, err)
		} else if !bytes.Equal(got, wantBytes) {
			t.Errorf("%s differs from golden", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// golden holds exactly the files the CLI writes, plus the report.
	err = filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if rel, _ := filepath.Rel(out, p); !want[rel] && rel != "report.md" {
			t.Errorf("unexpected output file %s", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := os.ReadFile(filepath.Join(out, "report.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"## Summary", "## Manual follow-up", "## Skipped files", "## Data lookups", "## Converted objects", "## Notes by file"} {
		if !strings.Contains(string(report), "\n"+h+"\n") {
			t.Errorf("report lacks %q:\n%s", h, report)
		}
	}
	for _, s := range []string{"midpoint_role.", "simple-roles.tmpl.json", "dropped as server-managed"} {
		if !strings.Contains(string(report), s) {
			t.Errorf("report lacks %q:\n%s", s, report)
		}
	}

	// A second run into the non-empty directory is refused, nothing is touched.
	if code, stderr := run(t, "--out", out, "../../internal/convert/testdata/catalogue"); code != 1 || !strings.Contains(stderr, "not empty") {
		t.Errorf("rerun: exit %d, stderr %q", code, stderr)
	}
}

func TestMain_conflictExit2(t *testing.T) {
	in := t.TempDir()
	oid := "11111111-2222-4333-8444-555555555555"
	a, b := filepath.Join(in, "a", "role.json"), filepath.Join(in, "b", "role.json")
	for p, name := range map[string]string{a: "one", b: "two"} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		body := `{"role":{"oid":"` + oid + `","name":"` + name + `"}}`
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(t.TempDir(), "out")
	code, stderr := run(t, "--out", out, in)
	if code != 2 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stderr, a) || !strings.Contains(stderr, b) {
		t.Errorf("stderr does not name both files: %q", stderr)
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("out was created despite the conflict")
	}
}

func TestMain_usageExit1(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"--out", t.TempDir()},
		{"--format", "xml", "--out", filepath.Join(t.TempDir(), "o"), "."},
		{"--out", filepath.Join(t.TempDir(), "o"), "/does/not/exist"},
	} {
		if code, stderr := run(t, args...); code != 1 || stderr == "" {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
}

func TestWriteFiles_failureLeavesOutUntouched(t *testing.T) {
	parent := t.TempDir()
	out := filepath.Join(parent, "out")
	// "a" is a file, so "a/b" cannot be written: the failure comes midway.
	err := writeFiles(out, map[string][]byte{"a": []byte("x"), "a/b": []byte("y"), "z": []byte("z")})
	if err == nil {
		t.Fatal("want an error")
	}
	entries, _ := os.ReadDir(parent)
	if len(entries) != 0 {
		t.Errorf("parent holds %v after a failed write", entries)
	}

	// An existing empty out stays empty and usable.
	if err := os.Mkdir(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFiles(out, map[string][]byte{"a": []byte("x"), "a/b": []byte("y")}); err == nil {
		t.Fatal("want an error")
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("out holds %v after a failed write", entries)
	}
	if err := checkOut(out); err != nil {
		t.Errorf("rerun would be refused: %v", err)
	}
	if err := writeFiles(out, map[string][]byte{"a": []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, "a")); string(b) != "x" {
		t.Errorf("a = %q", b)
	}
}

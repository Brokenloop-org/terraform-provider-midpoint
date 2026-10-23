package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// accTerraform runs the terraform binary on PATH in one working directory
// against this provider, built from the working tree and installed through
// a dev_overrides CLI config, the way the Makefile's validate-examples does.
// It is for the tests that need plain terraform commands (an import plan,
// state rm), which terraform-plugin-testing does not expose.
type accTerraform struct {
	t   *testing.T
	dir string
	env []string
}

// accProviderBlock is the provider configuration for a config directory run
// by accTerraform: the source of the dev override and a provider "midpoint"
// configured from the MIDPOINT_* environment variables.
const accProviderBlock = `terraform {
  required_providers {
    midpoint = { source = "jplana/midpoint" }
  }
}

provider "midpoint" {}
`

// accBuildProvider builds the provider into a temporary directory and
// returns the environment for terraform commands that use it: the caller's
// environment (the MIDPOINT_* variables among it) plus a CLI config with
// the dev override.
func accBuildProvider(t *testing.T) []string {
	t.Helper()
	if _, err := exec.LookPath("terraform"); err != nil {
		t.Fatalf("the terraform binary must be on PATH: %v", err)
	}
	bin := t.TempDir()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(bin, "terraform-provider-midpoint"), ".")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the provider: %v\n%s", err, out)
	}
	rc := filepath.Join(bin, "terraformrc")
	cfg := fmt.Sprintf("provider_installation {\n  dev_overrides {\n    \"jplana/midpoint\" = %q\n  }\n  direct {}\n}\n", bin)
	if err := os.WriteFile(rc, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return append(os.Environ(),
		"TF_CLI_CONFIG_FILE="+rc,
		"TF_IN_AUTOMATION=1",
		"TF_INPUT=0",
		"CHECKPOINT_DISABLE=1",
	)
}

// newAccTerraform returns a runner for dir, with env from accBuildProvider.
func newAccTerraform(t *testing.T, env []string, dir string) *accTerraform {
	return &accTerraform{t: t, dir: dir, env: env}
}

// try runs terraform with args and returns its stdout, and an error that
// carries stderr when the command fails.
func (tf *accTerraform) try(args ...string) ([]byte, error) {
	cmd := exec.Command("terraform", append([]string{"-chdir=" + tf.dir}, args...)...)
	cmd.Env = tf.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("terraform %v: %v\n%s%s", args, err, stdout.String(), stderr.String())
	}
	return stdout.Bytes(), nil
}

// run is try that fails the test on error.
func (tf *accTerraform) run(args ...string) []byte {
	tf.t.Helper()
	out, err := tf.try(args...)
	if err != nil {
		tf.t.Fatal(err)
	}
	return out
}

// tfShowValues is the part of `terraform show -json` this package reads,
// for a state and for a plan (whose prior_state has the same shape).
type tfShowValues struct {
	Values *struct {
		RootModule struct {
			Resources []tfShowResource `json:"resources"`
		} `json:"root_module"`
	} `json:"values"`
}

// tfShowResource is one resource or data source instance of a state.
type tfShowResource struct {
	Address string         `json:"address"`
	Mode    string         `json:"mode"`
	Type    string         `json:"type"`
	Values  map[string]any `json:"values"`
}

// tfPlan is the part of `terraform show -json <plan>` this package reads.
type tfPlan struct {
	PriorState      tfShowValues `json:"prior_state"`
	ResourceChanges []struct {
		Address string `json:"address"`
		Mode    string `json:"mode"`
		Type    string `json:"type"`
		Change  struct {
			Actions []string `json:"actions"`
			Before  any      `json:"before"`
			After   any      `json:"after"`
			// AfterUnknown marks the values only known after apply.
			AfterUnknown any             `json:"after_unknown"`
			Importing    json.RawMessage `json:"importing"`
		} `json:"change"`
	} `json:"resource_changes"`
}

// stateResources returns the resource and data source instances of the
// working directory's state; none when there is no state.
func (tf *accTerraform) stateResources() ([]tfShowResource, error) {
	out, err := tf.try("show", "-json", "-no-color")
	if err != nil {
		return nil, err
	}
	var st tfShowValues
	if err := json.Unmarshal(out, &st); err != nil {
		return nil, fmt.Errorf("parsing terraform show -json: %w", err)
	}
	if st.Values == nil {
		return nil, nil
	}
	return st.Values.RootModule.Resources, nil
}

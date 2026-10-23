package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/client"
	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/normalize"
)

// The CSV connector that ships with the midPoint image.
const accCSVConnectorType = "com.evolveum.polygon.connector.csv.CsvConnector"

// accCSV is the CSV resource fixture and the CSV connector of this
// environment.
type accCSV struct {
	dir string // absolute path of testacc/fixtures/csv-resource
	// connectorVersion is the version of the one CSV connector the image
	// ships; it differs between midPoint releases (2.8 in 4.9, 2.9 in
	// 4.10), so it is looked up, never hard-coded.
	connectorVersion string
}

// accCSVFixtures returns the CSV fixture directory and looks up the version
// of the CSV connector installed in the environment, of which there must be
// exactly one.
func accCSVFixtures(t *testing.T, c *client.Client) accCSV {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testacc", "fixtures", "csv-resource"))
	if err != nil {
		t.Fatal(err)
	}
	return accCSV{dir: dir, connectorVersion: accCSVConnectorVersion(t, c)}
}

// accCSVConnectorVersion is the version of the one CSV connector midPoint
// discovered in this environment.
func accCSVConnectorVersion(t *testing.T, c *client.Client) string {
	t.Helper()
	connectors, _, err := c.Search(context.Background(), "connectors",
		fmt.Sprintf(`connectorType = %q`, accCSVConnectorType), 2, nil)
	if err != nil || len(connectors) != 1 {
		t.Fatalf("looking up the CSV connector: %v (found %d, want 1)", err, len(connectors))
	}
	version, _ := connectors[0].Body["connectorVersion"].(string)
	if version == "" {
		t.Fatalf("the CSV connector %s has no connectorVersion", connectors[0].OID)
	}
	return version
}

// accCSVResourceConfig is a midpoint_resource "csv" for the CSV fixture
// (testacc/fixtures/data/accounts.csv, mounted into the server) with the
// given schema handling file and extra attribute lines.
func accCSVResourceConfig(fixtures accCSV, name, schemaHandling, attrs string) string {
	return fmt.Sprintf(`
resource "midpoint_resource" "csv" {
  name        = %q
  description = "Acceptance CSV resource"
  connector = {
    type    = %q
    version = %q
  }
  connector_configuration = yamldecode(file(%q))
  schema_handling         = yamldecode(file(%q))
%s
}
`, name, accCSVConnectorType, fixtures.connectorVersion,
		filepath.Join(fixtures.dir, "connector-configuration.yaml"), filepath.Join(fixtures.dir, schemaHandling), attrs)
}

// accGetResource reads resources/oid with noFetch (no connector call, so
// the read changes nothing) in canonical form, "@id"s kept; nil when it
// does not exist.
func accGetResource(c *client.Client, oid string) (map[string]any, error) {
	obj, err := c.Get(context.Background(), "resources", oid, client.GetOptions{Options: []string{"noFetch"}, Exclude: readExcludes})
	if errors.Is(err, client.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return normalize.Canonical("resources", obj.Body), nil
}

// accResourcePost POSTs to a resource action below the REST base path
// (e.g. "resources/<oid>/test") and returns the response; the client has
// no call for operations that are not CRUD.
func accResourcePost(action string) (*http.Response, []byte, error) {
	u, err := url.Parse(strings.TrimRight(os.Getenv("MIDPOINT_URL"), "/") + "/midpoint/ws/rest/" + action)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, u.String(), nil)
	if err != nil {
		return nil, nil, err
	}
	req.SetBasicAuth(os.Getenv("MIDPOINT_USERNAME"), os.Getenv("MIDPOINT_PASSWORD"))
	req.Header.Set("Accept", "application/json")
	// The import action answers 303 to the task; keep that response.
	cl := &http.Client{Timeout: time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp, body, err
}

// accResourceOID returns the oid of addr in st.
func accResourceOID(st *terraform.State, addr string) (string, error) {
	rs, ok := st.RootModule().Resources[addr]
	if !ok {
		return "", fmt.Errorf("%s not in state", addr)
	}
	return rs.Primary.Attributes["oid"], nil
}

// accResourceGone is a CheckDestroy that every midpoint_resource in the
// state is gone from the server.
func accResourceGone(c *client.Client) resource.TestCheckFunc {
	return func(st *terraform.State) error {
		for _, rs := range st.RootModule().Resources {
			if rs.Type != "midpoint_resource" {
				continue
			}
			body, err := accGetResource(c, rs.Primary.Attributes["oid"])
			if err != nil {
				return err
			}
			if body != nil {
				return fmt.Errorf("resources/%s still exists", rs.Primary.Attributes["oid"])
			}
		}
		return nil
	}
}

// fullNameAttribute returns the ri:fullName attribute of the only object
// type of a canonical resource body.
func fullNameAttribute(body map[string]any) (map[string]any, error) {
	sh, _ := body["schemaHandling"].(map[string]any)
	types := asList(sh["objectType"])
	if len(types) != 1 {
		return nil, fmt.Errorf("want one object type, got %v", sh["objectType"])
	}
	ot, _ := types[0].(map[string]any)
	for _, a := range asList(ot["attribute"]) {
		if am, _ := a.(map[string]any); am["ref"] == "ri:fullName" {
			return am, nil
		}
	}
	return nil, fmt.Errorf("no ri:fullName attribute in %v", ot)
}

// accCSVConfiguredParts configures capabilities and generation
// constraints on the CSV resource.
const accCSVConfiguredParts = `
  capabilities = { create = { enabled = false } }
  schema       = { generation_constraints = { generateObjectClass = ["ri:AccountObjectClass"] } }
`

// accServerManagedKept checks, on a raw read of the resource at addr, that
// midPoint still holds the native capabilities and the cached schema
// definition, and that the configured capabilities and generation
// constraints are present exactly when configured is set.
func accServerManagedKept(c *client.Client, addr string, configured bool) resource.TestCheckFunc {
	return func(st *terraform.State) error {
		oid, err := accResourceOID(st, addr)
		if err != nil {
			return err
		}
		raw, err := c.Get(context.Background(), "resources", oid, client.GetOptions{Options: []string{"noFetch"}})
		if err != nil {
			return err
		}
		caps, _ := raw.Body["capabilities"].(map[string]any)
		sch, _ := raw.Body["schema"].(map[string]any)
		def, _ := sch["definition"].(map[string]any)
		if caps["native"] == nil || def["schema"] == nil {
			return fmt.Errorf("server-managed parts lost: capabilities %v, schema keys %v", caps, keysOf(sch))
		}
		if (caps["configured"] != nil) != configured || (sch["generationConstraints"] != nil) != configured {
			return fmt.Errorf("configured parts: capabilities/configured %v, schema/generationConstraints %v, want present = %v",
				caps["configured"], sch["generationConstraints"], configured)
		}
		return nil
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestAccResource_csv(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	fixtures := accCSVFixtures(t, c)
	name := acctest.RandomWithPrefix("tf-acc-csv")
	// The narrow-update step counts the operations an apply sent by their
	// operationExecution records.
	accRecordSuccessfulOperations(t, c)
	const addr = "midpoint_resource.csv"

	// The connector OID of this environment, looked up the way midPoint
	// resolves the filter; never hard-coded.
	connectors, _, err := c.Search(context.Background(), "connectors",
		fmt.Sprintf(`connectorType = %q and connectorVersion = %q`, accCSVConnectorType, fixtures.connectorVersion), 2, nil)
	if err != nil || len(connectors) != 1 {
		t.Fatalf("looking up the CSV connector: %v (found %d)", err, len(connectors))
	}
	connectorOID := connectors[0].OID

	var before map[string]any
	var beforeExecutions []execution
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accResourceGone(c),
		Steps: []resource.TestStep{
			{
				Config: accCSVResourceConfig(fixtures, name, "schema-handling.yaml", ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("connector_oid"), knownvalue.StringExact(connectorOID)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("capabilities"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("schema"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("extra"), knownvalue.Null()),
				},
				Check: func(st *terraform.State) error {
					oid, err := accResourceOID(st, addr)
					if err != nil {
						return err
					}
					body, err := accGetResource(c, oid)
					if err != nil || body == nil {
						return fmt.Errorf("reading the resource: %v (found %v)", err, body != nil)
					}
					ref, _ := body["connectorRef"].(map[string]any)
					if ref["oid"] != connectorOID {
						return fmt.Errorf("connectorRef = %v, want resolved to %s", ref, connectorOID)
					}
					return nil
				},
			},
			emptyPlanAfterRefresh(),
			// A connection test makes midPoint fetch and cache the schema
			// and native capabilities and record the operational state:
			// none of it is drift.
			{
				PreConfig: func() {
					st := accStateOID(t, c, name)
					resp, body, err := accResourcePost("resources/" + st + "/test")
					if err != nil || resp.StatusCode != http.StatusOK {
						t.Fatalf("testing the resource: %v %v", err, resp)
					}
					var result struct {
						Object struct {
							Status string `json:"status"`
						} `json:"object"`
					}
					if err := json.Unmarshal(body, &result); err != nil || result.Object.Status != "success" {
						t.Fatalf("resource test did not succeed (%v): %.500s", err, body)
					}
					b, err := accGetResource(c, st)
					if err != nil {
						t.Fatal(err)
					}
					raw, err := c.Get(context.Background(), "resources", st, client.GetOptions{Options: []string{"noFetch"}})
					if err != nil {
						t.Fatal(err)
					}
					sch, _ := raw.Body["schema"].(map[string]any)
					caps, _ := raw.Body["capabilities"].(map[string]any)
					if sch["definition"] == nil || caps["native"] == nil || raw.Body["operationalState"] == nil {
						t.Fatalf("the connection test cached no schema, native capabilities or state: %v", b)
					}
				},
				Config: accCSVResourceConfig(fixtures, name, "schema-handling.yaml", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("capabilities"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("schema"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("extra"), knownvalue.Null()),
				},
			},
			// Configuring capabilities and generation constraints next to
			// the native capabilities and cached schema, and removing them
			// again, touches only the configured parts.
			{
				Config: accCSVResourceConfig(fixtures, name, "schema-handling.yaml", accCSVConfiguredParts),
				Check:  accServerManagedKept(c, addr, true),
			},
			emptyPlanAfterRefresh(),
			{
				Config: accCSVResourceConfig(fixtures, name, "schema-handling.yaml", ""),
				Check:  accServerManagedKept(c, addr, false),
			},
			emptyPlanAfterRefresh(),
			// One more inbound mapping on ri:fullName is one narrow add:
			// one modification (the version moves by one) that keeps every
			// container id and adds one inbound value.
			{
				PreConfig: func() {
					oid := accStateOID(t, c, name)
					var err error
					before, err = accGetResource(c, oid)
					if err != nil || before == nil {
						t.Fatalf("reading the resource: %v", err)
					}
					if beforeExecutions, err = accResourceExecutions(c, oid); err != nil {
						t.Fatal(err)
					}
				},
				Config: accCSVResourceConfig(fixtures, name, "schema-handling-extra-inbound.yaml", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: func(st *terraform.State) error {
					oid, err := accResourceOID(st, addr)
					if err != nil {
						return err
					}
					after, err := accGetResource(c, oid)
					if err != nil {
						return err
					}
					executions, err := accResourceExecutions(c, oid)
					if err != nil {
						return err
					}
					if added := newExecutions(beforeExecutions, executions); len(added) != 1 || added[0].Change != "modify" {
						return fmt.Errorf("operations recorded by the update: %v, want exactly one modify", added)
					}
					a1, err := fullNameAttribute(before)
					if err != nil {
						return err
					}
					a2, err := fullNameAttribute(after)
					if err != nil {
						return err
					}
					in1, in2 := asList(a1["inbound"]), asList(a2["inbound"])
					if a1["@id"] != a2["@id"] || len(in1) != 1 || len(in2) != 2 {
						return fmt.Errorf("ri:fullName %v -> %v, want the same attribute with one more inbound", a1, a2)
					}
					old, _ := in1[0].(map[string]any)
					kept, _ := in2[0].(map[string]any)
					added, _ := in2[1].(map[string]any)
					if old["@id"] != kept["@id"] || added["name"] != "fullname-to-description" {
						return fmt.Errorf("inbound %v -> %v, want the first kept and fullname-to-description added", in1, in2)
					}
					// Everything outside ri:fullName's inbound list is untouched.
					b, a := stateBody(before), stateBody(after)
					fb, _ := fullNameAttribute(b)
					fa, _ := fullNameAttribute(a)
					delete(fb, "inbound")
					delete(fa, "inbound")
					if !normalize.Equal(b, a) {
						bj, _ := json.Marshal(b)
						aj, _ := json.Marshal(a)
						return fmt.Errorf("the update changed more than the inbound list:\n%s\n%s", bj, aj)
					}
					return nil
				},
			},
			emptyPlanAfterRefresh(),
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{
					// An import reads back no connector (connector_oid
					// holds the resolved reference) ...
					"connector",
					// ... the configurationProperties without their "@ns"
					// (an annotation, never compared) ...
					"connector_configuration.configurationProperties.@ns",
					// ... and the single objectClass in canonical (list)
					// form.
					"schema_handling.objectType.0.objectClass",
				},
			},
			// A mapping edited in midPoint (as in the GUI) is drift, and the
			// apply restores it.
			{
				PreConfig: func() {
					oid := accStateOID(t, c, name)
					body, err := accGetResource(c, oid)
					if err != nil {
						t.Fatal(err)
					}
					sh, _ := body["schemaHandling"].(map[string]any)
					ot, _ := asList(sh["objectType"])[0].(map[string]any)
					attr, err := fullNameAttribute(body)
					if err != nil {
						t.Fatal(err)
					}
					in, _ := asList(attr["inbound"])[0].(map[string]any)
					p := fmt.Sprintf("schemaHandling/objectType[%v]/attribute[%v]/inbound[%v]/target", ot["@id"], attr["@id"], in["@id"])
					if err := c.Modify(context.Background(), "resources", oid, []client.ItemDelta{
						{ModificationType: "replace", Path: p, Value: map[string]any{"path": "nickName"}},
					}, nil); err != nil {
						t.Fatal(err)
					}
				},
				Config:             accCSVResourceConfig(fixtures, name, "schema-handling-extra-inbound.yaml", ""),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: accCSVResourceConfig(fixtures, name, "schema-handling-extra-inbound.yaml", ""),
				Check: func(st *terraform.State) error {
					oid, err := accResourceOID(st, addr)
					if err != nil {
						return err
					}
					body, err := accGetResource(c, oid)
					if err != nil {
						return err
					}
					attr, err := fullNameAttribute(body)
					if err != nil {
						return err
					}
					// The edited value is deleted and the configured one
					// added, so look it up by name.
					for _, v := range asList(attr["inbound"]) {
						in, _ := v.(map[string]any)
						if in["name"] != "fullname-to-fullname" {
							continue
						}
						if target, _ := in["target"].(map[string]any); target["path"] != "fullName" {
							return fmt.Errorf("inbound target = %v, want fullName restored", in["target"])
						}
						return nil
					}
					return fmt.Errorf("no inbound fullname-to-fullname in %v", attr["inbound"])
				},
			},
			emptyPlanAfterRefresh(),
		},
	})
}

// accStateOID finds the oid of the resource named name.
func accStateOID(t *testing.T, c *client.Client, name string) string {
	t.Helper()
	objs, _, err := c.Search(context.Background(), "resources", fmt.Sprintf("name = %q", name), 2, nil)
	if err != nil || len(objs) != 1 {
		t.Fatalf("finding resource %s: %v (found %d)", name, err, len(objs))
	}
	return objs[0].OID
}

// execution is one operation execution record: when midPoint executed an
// operation on an object, and its change type (add, modify).
type execution struct {
	At     time.Time
	Change string
}

// accResourceExecutions lists the operation execution records of
// resources/oid, one per operation midPoint executed on it. (The version is
// no count of operations: midPoint 4.9 stores the record of a modification
// in a second repository update, so one PATCH moves the version by two.)
func accResourceExecutions(c *client.Client, oid string) ([]execution, error) {
	obj, err := c.Get(context.Background(), "resources", oid, client.GetOptions{Options: []string{"noFetch"}})
	if err != nil {
		return nil, err
	}
	var out []execution
	for _, e := range asList(obj.Body["operationExecution"]) {
		m, _ := e.(map[string]any)
		op, _ := m["operation"].(map[string]any)
		od, _ := op["objectDelta"].(map[string]any)
		at, err := time.Parse(time.RFC3339Nano, fmt.Sprint(m["timestamp"]))
		if err != nil {
			return nil, fmt.Errorf("resources/%s operation execution timestamp: %w", oid, err)
		}
		out = append(out, execution{At: at, Change: fmt.Sprint(od["changeType"])})
	}
	return out, nil
}

// accSystemConfigurationOID is the OID of midPoint's system configuration.
const accSystemConfigurationOID = "00000000-0000-0000-0000-000000000001"

// accRecordSuccessfulOperations makes midPoint record successful operations
// in operationExecution until the test ends. midPoint 4.9 records them; the
// initial system configuration of a midPoint 4.10 repository sets
// internals/operationExecutionRecording/skipWhenSuccess, so only failures
// are recorded there. The setting is restored at cleanup.
func accRecordSuccessfulOperations(t *testing.T, c *client.Client) {
	t.Helper()
	ctx := context.Background()
	obj, err := c.Get(ctx, "systemConfigurations", accSystemConfigurationOID, client.GetOptions{})
	if err != nil {
		t.Fatalf("reading the system configuration: %v", err)
	}
	internals, _ := obj.Body["internals"].(map[string]any)
	recording, _ := internals["operationExecutionRecording"].(map[string]any)
	if recording["skipWhenSuccess"] != true {
		return
	}
	const path = "internals/operationExecutionRecording/skipWhenSuccess"
	set := func(skip bool) error {
		return c.Modify(ctx, "systemConfigurations", accSystemConfigurationOID,
			[]client.ItemDelta{{ModificationType: "replace", Path: path, Value: skip}}, nil)
	}
	if err := set(false); err != nil {
		t.Fatalf("enabling the recording of successful operations: %v", err)
	}
	t.Cleanup(func() {
		if err := set(true); err != nil {
			t.Errorf("cleanup: restoring %s: %v", path, err)
		}
	})
}

// newExecutions returns the records of after that are later than every
// record of before (midPoint may prune old records).
func newExecutions(before, after []execution) []execution {
	var latest time.Time
	for _, b := range before {
		if b.At.After(latest) {
			latest = b.At
		}
	}
	var out []execution
	for _, a := range after {
		if a.At.After(latest) {
			out = append(out, a)
		}
	}
	return out
}

// An import of the CSV accounts gives the resource shadows: a destroy is
// refused until force_destroy is set.
func TestAccResource_forceDestroyWithShadows(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	fixtures := accCSVFixtures(t, c)
	name := acctest.RandomWithPrefix("tf-acc-csv-shadows")
	var oid, importTask string
	// The resource delete leaves the shadows in the repository (midPoint
	// 4.9 does not delete them with the resource); remove them, the import
	// task and, when a step failed before the destroy, the resource.
	t.Cleanup(func() {
		ctx := context.Background()
		if oid != "" {
			shadows, _, err := c.Search(ctx, "shadows", shadowsFilter(oid), 50, []string{"raw"})
			if err != nil {
				t.Errorf("finding the test shadows: %v", err)
			}
			for _, s := range shadows {
				if err := c.Delete(ctx, "shadows", s.OID, []string{"raw"}); err != nil && !errors.Is(err, client.ErrNotFound) {
					t.Errorf("deleting shadow %s: %v", s.OID, err)
				}
			}
			if err := c.Delete(ctx, "resources", oid, []string{"raw"}); err != nil && !errors.Is(err, client.ErrNotFound) {
				t.Errorf("deleting resource %s: %v", oid, err)
			}
		}
		if importTask != "" {
			if err := c.Delete(ctx, "tasks", importTask, nil); err != nil && !errors.Is(err, client.ErrNotFound) {
				t.Errorf("deleting import task %s: %v", importTask, err)
			}
		}
	})
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accResourceGone(c),
		Steps: []resource.TestStep{
			{
				Config: accCSVResourceConfig(fixtures, name, "schema-handling.yaml", ""),
				Check: func(st *terraform.State) error {
					var err error
					if oid, err = accResourceOID(st, "midpoint_resource.csv"); err != nil {
						return err
					}
					resp, body, err := accResourcePost("resources/" + oid + "/import/AccountObjectClass")
					if err != nil {
						return err
					}
					if resp.StatusCode != http.StatusSeeOther {
						return fmt.Errorf("import: HTTP %d: %.500s", resp.StatusCode, body)
					}
					importTask = path.Base(resp.Header.Get("Location"))
					// The import task runs in the background; two accounts.
					deadline := time.Now().Add(time.Minute)
					for {
						shadows, _, err := c.Search(context.Background(), "shadows", shadowsFilter(oid), 5, []string{"raw"})
						if err != nil {
							return err
						}
						if len(shadows) >= 2 {
							return nil
						}
						if time.Now().After(deadline) {
							return fmt.Errorf("the import made %d shadows within a minute, want 2", len(shadows))
						}
						time.Sleep(500 * time.Millisecond)
					}
				},
			},
			{
				Config:      accCSVResourceConfig(fixtures, name, "schema-handling.yaml", ""),
				Destroy:     true,
				ExpectError: regexp.MustCompile(`still\s+reference\s+it`),
			},
			{Config: accCSVResourceConfig(fixtures, name, "schema-handling.yaml", "  force_destroy = true")},
		},
	})
}

// shadowsFilter is the Axiom filter of the shadows of resource oid.
func shadowsFilter(oid string) string {
	_, filter := shadowsCheck(oid)
	return filter
}

// exampleConnectorVersion matches the connector version line of the
// midpoint_resource example.
var exampleConnectorVersion = regexp.MustCompile(`(?m)^(\s*version\s*=\s*)"[^"]*"`)

// The documented example (examples/resources/midpoint_resource, with its
// YAML parts: correlation and synchronization reactions) applies and reads
// back without drift.
func TestAccResource_example(t *testing.T) {
	testAccPreCheck(t)
	c := accClient(t)
	dir, err := filepath.Abs(filepath.Join("..", "..", "examples", "resources", "midpoint_resource"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "resource.tf"))
	if err != nil {
		t.Fatal(err)
	}
	// The test runs the configuration from a temporary directory, and
	// under a test name.
	const exampleName = `name        = "HR"`
	if !strings.Contains(string(raw), exampleName) {
		t.Fatalf("the example no longer sets %s", exampleName)
	}
	cfg := strings.ReplaceAll(string(raw), "${path.module}", dir)
	cfg = strings.Replace(cfg, exampleName, fmt.Sprintf("name        = %q", acctest.RandomWithPrefix("tf-acc-example")), 1)
	// The example pins the CSV connector of the 4.9 image; the test pins
	// the one this environment ships, so it runs on every midPoint version.
	if n := len(exampleConnectorVersion.FindAllString(cfg, -1)); n != 1 {
		t.Fatalf("the example sets the connector version %d times, want 1", n)
	}
	cfg = exampleConnectorVersion.ReplaceAllString(cfg, fmt.Sprintf("${1}%q", accCSVConnectorVersion(t, c)))
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { testAccPreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             accResourceGone(c),
		Steps: []resource.TestStep{
			{Config: cfg},
			emptyPlanAfterRefresh(),
		},
	})
}

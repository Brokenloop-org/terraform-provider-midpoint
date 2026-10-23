package provider

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"

	"github.com/Brokenloop-org/terraform-provider-midpoint/internal/mpstub"
)

const (
	dsCSVType       = "com.evolveum.polygon.connector.csv.CsvConnector"
	dsLDAPType      = "com.evolveum.polygon.connector.ldap.LdapConnector"
	dsConnectorAddr = "data.midpoint_connector.test"
)

// putConnectors stores two versions of the CSV connector and one LDAP
// connector, and returns their oids in that order.
func putConnectors(s *mpstub.Server) (csv27, csv28, ldap string) {
	csv27 = s.Put("connectors", map[string]any{
		"name":          "ConnId " + dsCSVType + " v2.7",
		"connectorType": dsCSVType, "connectorVersion": "2.7",
	})
	csv28 = s.Put("connectors", map[string]any{
		"name":          map[string]any{"orig": "ConnId " + dsCSVType + " v2.8", "norm": "connid csv v2.8"},
		"connectorType": dsCSVType, "connectorVersion": "2.8",
	})
	ldap = s.Put("connectors", map[string]any{
		"name":          "ConnId " + dsLDAPType + " v3.9",
		"connectorType": dsLDAPType, "connectorVersion": "3.9",
	})
	return csv27, csv28, ldap
}

func connectorConfig(attrs string) string {
	return `
data "midpoint_connector" "test" {
` + attrs + `
}
`
}

func TestDataConnector_byType(t *testing.T) {
	s := newTestStub(t)
	_, csv28, ldap := putConnectors(s)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				// The only connector of its type: the version is computed.
				Config: connectorConfig(`connector_type = "` + dsLDAPType + `"`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(dsConnectorAddr, tfjsonpath.New("id"), knownvalue.StringExact(ldap)),
					statecheck.ExpectKnownValue(dsConnectorAddr, tfjsonpath.New("oid"), knownvalue.StringExact(ldap)),
					statecheck.ExpectKnownValue(dsConnectorAddr, tfjsonpath.New("connector_version"), knownvalue.StringExact("3.9")),
					statecheck.ExpectKnownValue(dsConnectorAddr, tfjsonpath.New("name"), knownvalue.StringExact("ConnId "+dsLDAPType+" v3.9")),
				},
				Check: checkStub(func() error {
					return expectSearch(s, "/connectors/search", `connectorType = "`+dsLDAPType+`"`)
				}),
			},
			{
				// A version picks one of several.
				Config: connectorConfig(`
  connector_type    = "` + dsCSVType + `"
  connector_version = "2.8"
`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(dsConnectorAddr, tfjsonpath.New("oid"), knownvalue.StringExact(csv28)),
					statecheck.ExpectKnownValue(dsConnectorAddr, tfjsonpath.New("connector_version"), knownvalue.StringExact("2.8")),
					statecheck.ExpectKnownValue(dsConnectorAddr, tfjsonpath.New("name"), knownvalue.StringExact("ConnId "+dsCSVType+" v2.8")),
				},
			},
		},
	})
}

func TestDataConnector_ambiguous(t *testing.T) {
	s := newTestStub(t)
	csv27, csv28, _ := putConnectors(s)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: connectorConfig(`connector_type = "` + dsCSVType + `"`),
			ExpectError: regexp.MustCompile(`(?s)more\s+than\s+one\s+connector.*version\s+2\.7.*` + csv27 +
				`.*version\s+2\.8.*` + csv28 + `.*connector_version`),
		}},
	})
}

func TestDataConnector_notFound(t *testing.T) {
	s := newTestStub(t)
	putConnectors(s)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{
			{
				Config:      connectorConfig(`connector_type = "com.example.Missing"`),
				ExpectError: regexp.MustCompile(`no\s+connector\s+of\s+type\s+com\.example\.Missing`),
			},
			{
				// A missing version lists the versions there are.
				Config: connectorConfig(`
  connector_type    = "` + dsCSVType + `"
  connector_version = "9.9"
`),
				ExpectError: regexp.MustCompile(`(?s)no\s+connector\s+of\s+type\s+` + regexp.QuoteMeta(dsCSVType) +
					`\s+with\s+version\s+9\.9.*available\s+versions.*2\.7.*2\.8`),
			},
		},
	})
}

func TestDataConnector_ambiguousWithVersion(t *testing.T) {
	s := newTestStub(t)
	putConnectors(s)
	twin := s.Put("connectors", map[string]any{
		"name":          "ConnId " + dsLDAPType + " v3.9 (copy)",
		"connectorType": dsLDAPType, "connectorVersion": "3.9",
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: connectorConfig(`
  connector_type    = "` + dsLDAPType + `"
  connector_version = "3.9"
`),
			ExpectError: regexp.MustCompile(`(?s)more\s+than\s+one\s+connector.*` + twin + `.*use\s+the\s+oid\s+directly`),
		}},
	})
}

func TestDataConnector_notFoundWhenListingVersionsFails(t *testing.T) {
	s := newTestStub(t)
	putConnectors(s)
	// Fail only the follow-up search by type alone.
	s.SetFault(func(r mpstub.Recorded) int {
		query, _ := r.Body["query"].(map[string]any)
		filter, _ := query["filter"].(map[string]any)
		if text := fmt.Sprint(filter["text"]); strings.HasPrefix(text, "connectorType") && !strings.Contains(text, "connectorVersion") {
			return http.StatusInternalServerError
		}
		return 0
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: stubProviderFactories(t, s),
		Steps: []resource.TestStep{{
			Config: connectorConfig(`
  connector_type    = "` + dsCSVType + `"
  connector_version = "9.9"
`),
			ExpectError: regexp.MustCompile(`(?s)Connector\s+not\s+found.*no\s+connector\s+of\s+type.*with\s+version\s+9\.9.*` +
				`cannot\s+list\s+the\s+available\s+versions.*500`),
		}},
	})
}

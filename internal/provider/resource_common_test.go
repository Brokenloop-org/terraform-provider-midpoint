package provider

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/jplana/terraform-provider-midpoint/internal/mpstub"
)

// stubProviderFactories returns provider factories whose provider talks to
// the stub s, configured through the MIDPOINT_* environment variables. Use
// them with resource.UnitTest: the tests need no TF_ACC and no server.
func stubProviderFactories(t *testing.T, s *mpstub.Server) map[string]func() (tfprotov6.ProviderServer, error) {
	t.Helper()
	t.Setenv("MIDPOINT_URL", s.URL())
	t.Setenv("MIDPOINT_USERNAME", "administrator")
	t.Setenv("MIDPOINT_PASSWORD", "secret")
	t.Setenv("MIDPOINT_INSECURE", "")
	return map[string]func() (tfprotov6.ProviderServer, error){
		"midpoint": providerserver.NewProtocol6WithError(New("test")()),
	}
}

// newTestStub starts a stub that is closed when the test ends.
func newTestStub(t *testing.T) *mpstub.Server {
	t.Helper()
	s := mpstub.New()
	t.Cleanup(s.Close)
	return s
}

func TestStateBody_dropsOIDAndContainerIDs(t *testing.T) {
	in := map[string]any{
		"oid":  "o",
		"name": "n",
		"inducement": []any{map[string]any{
			"@id":       json.Number("3"),
			"targetRef": map[string]any{"oid": "t", "relation": "org:default"},
			"condition": map[string]any{"@id": json.Number("4"), "source": []any{map[string]any{"path": "x"}}},
		}},
	}
	want := map[string]any{
		"name": "n",
		"inducement": []any{map[string]any{
			"targetRef": map[string]any{"oid": "t", "relation": "org:default"},
			"condition": map[string]any{"source": []any{map[string]any{"path": "x"}}},
		}},
	}
	if got := stateBody(in); !reflect.DeepEqual(got, want) {
		t.Errorf("stateBody = %v, want %v", got, want)
	}
	if in["oid"] != "o" || in["inducement"].([]any)[0].(map[string]any)["@id"] == nil {
		t.Error("stateBody modified its input")
	}
}

func TestStripProtected_removesProtectedStringsAndEmptiedContainers(t *testing.T) {
	in := map[string]any{
		"name": "n",
		"credentials": map[string]any{
			"password": map[string]any{
				"value": map[string]any{"encryptedData": map[string]any{"cipherValue": "x"}},
			},
			"nonce": map[string]any{"value": map[string]any{"hashedData": map[string]any{"digestValue": "y"}}},
		},
		"connectorConfiguration": map[string]any{
			"configurationProperties": map[string]any{
				"host":     "ldap.example.com",
				"password": map[string]any{"clearValue": "secret"},
				"token":    []any{map[string]any{"externalData": map[string]any{"key": "k"}}, "kept"},
			},
		},
		"extension": map[string]any{},
		"empty":     []any{},
	}
	want := map[string]any{
		"name": "n",
		"connectorConfiguration": map[string]any{
			"configurationProperties": map[string]any{
				"host":  "ldap.example.com",
				"token": []any{"kept"},
			},
		},
		// Containers that were empty already are not stripping's business.
		"extension": map[string]any{},
		"empty":     []any{},
	}
	if got := stripProtected(in); !reflect.DeepEqual(got, want) {
		t.Errorf("stripProtected = %v, want %v", got, want)
	}
	if _, ok := in["credentials"]; !ok {
		t.Error("stripProtected modified its input")
	}
}

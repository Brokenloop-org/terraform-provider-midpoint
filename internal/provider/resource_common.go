package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

// commonAttributes are the attributes every midPoint resource has:
// adopt_existing, force_destroy and options, described for spec.
func commonAttributes(spec engineSpec) map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"adopt_existing": schema.BoolAttribute{
			Description: "When an object with the configured oid already exists, adopt it into state and apply " +
				"the configuration to it as an update instead of failing. The configuration must describe the whole " +
				"object: items midPoint holds that are not configured (other than server-managed ones) are removed " +
				"on the first apply. The plan warns that the object will be adopted and lists the changes the " +
				"update will send. Meant for the midPoint built-ins a catalogue customizes. Without it, creating an " +
				"object whose oid already exists fails and nothing is written. Defaults to false.",
			Optional: true,
			Computed: true,
			Default:  booldefault.StaticBool(false),
		},
		"force_destroy": schema.BoolAttribute{
			Description: forceDestroyDescription(spec),
			Optional:    true,
			Computed:    true,
			Default:     booldefault.StaticBool(false),
		},
		"options": schema.ListAttribute{
			Description: optionsDescription(spec),
			Optional:    true,
			ElementType: types.StringType,
		},
	}
}

// forceDestroyDescription describes force_destroy for spec: what the delete
// check looks for, if anything.
func forceDestroyDescription(spec engineSpec) string {
	const members = "objects assigned to it or holding it in roleMembershipRef (members)"
	const shadows = "shadows of the resource"
	switch {
	case spec.ShortName == "object":
		return "Delete the object even while other objects still reference it. Without it, deleting an object of " +
			"type roles, services, orgs or archetypes fails while " + members + " exist, and deleting one of type " +
			"resources fails while " + shadows + " exist; other types are deleted without a check. Defaults to false."
	case spec.MembersCheck == nil:
		return "Has no effect on this type: it is deleted without a reference check. Defaults to false."
	case spec.ShortName == "resource":
		return "Delete the resource even while " + shadows + " exist. Without it, the delete fails and names " +
			"the first few shadows. Defaults to false."
	default:
		return "Delete the object even while " + members + " exist. Without it, the delete fails and names " +
			"the first few members. Defaults to false."
	}
}

// optionsDescription describes the options attribute with spec's built-in
// defaults.
func optionsDescription(spec engineSpec) string {
	list := func(opts []string) string {
		if len(opts) == 0 {
			return "none"
		}
		return strings.Join(opts, ", ")
	}
	return "midPoint execution options sent on create and update, e.g. raw, isImport, reevaluateSearchFilters. " +
		"When unset, the provider default_options entry for " + spec.ShortName + " applies, else the built-in " +
		"defaults: " + list(spec.CreateOptions) + " on create and " + list(spec.UpdateOptions) + " on update. " +
		"An empty list sends no options. overwrite is sent only when listed here; reevaluateSearchFilters is " +
		"added to any write that sends a reference with a filter and no oid."
}

// optionsFromList converts the options attribute: nil when it is null (use
// the defaults), an empty slice for an explicitly empty list.
func optionsFromList(ctx context.Context, l types.List) ([]string, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	out := []string{}
	diags := l.ElementsAs(ctx, &out, false)
	return out, diags
}

// configuredData returns the provider data handed to a resource's
// Configure; nil (without a diagnostic) before the provider is configured.
func configuredData(req resource.ConfigureRequest, resp *resource.ConfigureResponse) *providerData {
	return asProviderData(req.ProviderData, &resp.Diagnostics)
}

// configuredDataSourceData is configuredData for a data source's Configure.
func configuredDataSourceData(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) *providerData {
	return asProviderData(req.ProviderData, &resp.Diagnostics)
}

func asProviderData(pd any, diags *diag.Diagnostics) *providerData {
	if pd == nil {
		return nil
	}
	data, ok := pd.(*providerData)
	if !ok {
		diags.AddError("Unexpected provider data",
			fmt.Sprintf("expected *providerData, got %T; this is a provider bug", pd))
		return nil
	}
	return data
}

// canonicalName is the canonical name of body, null when it has none.
func canonicalName(restType string, body map[string]any) types.String {
	if s, ok := normalize.Canonical(restType, body)["name"].(string); ok {
		return types.StringValue(s)
	}
	return types.StringNull()
}

// stateBody is a canonical server body as stored in state: without the root
// oid (it is the oid attribute) and without container "@id"s (server
// identifiers, never configuration). It never mutates body.
func stateBody(body map[string]any) map[string]any {
	out := stripIDs(body).(map[string]any)
	delete(out, "oid")
	return out
}

func stripIDs(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			if k != "@id" {
				out[k] = stripIDs(e)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = stripIDs(e)
		}
		return out
	default:
		return v
	}
}

// protectedKeys mark a protected string value: a map holding any of them
// carries a secret (or its hash) and never belongs in state.
var protectedKeys = []string{"clearValue", "encryptedData", "hashedData", "externalData"}

// stripProtected returns v without protected strings: every map holding a
// protected key is removed, and so is every map or list that becomes empty
// by it. Containers that were empty already are kept. A root that is
// removed entirely becomes an empty map. It never mutates v.
func stripProtected(v any) any {
	out, keep := stripProtectedValue(v)
	if !keep {
		return map[string]any{}
	}
	return out
}

func stripProtectedValue(v any) (out any, keep bool) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range protectedKeys {
			if _, ok := t[k]; ok {
				return nil, false
			}
		}
		m := make(map[string]any, len(t))
		for k, e := range t {
			if c, keep := stripProtectedValue(e); keep {
				m[k] = c
			}
		}
		return m, len(m) > 0 || len(t) == 0
	case []any:
		l := make([]any, 0, len(t))
		for _, e := range t {
			if c, keep := stripProtectedValue(e); keep {
				l = append(l, c)
			}
		}
		return l, len(l) > 0 || len(t) == 0
	default:
		return v, true
	}
}

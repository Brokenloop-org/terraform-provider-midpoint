package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

// commonAttributes are the attributes every midPoint resource has:
// adopt_existing, force_destroy and options.
func commonAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"adopt_existing": schema.BoolAttribute{
			Description: "When an object with the configured oid already exists, adopt it into state and apply " +
				"the configured content as an update instead of failing. Defaults to false.",
			Optional: true,
			Computed: true,
			Default:  booldefault.StaticBool(false),
		},
		"force_destroy": schema.BoolAttribute{
			Description: "Delete the object even when other objects still reference it (role members, " +
				"resource shadows). Defaults to false.",
			Optional: true,
			Computed: true,
			Default:  booldefault.StaticBool(false),
		},
		"options": schema.ListAttribute{
			Description: "midPoint execution options sent on create and update (e.g. raw, isImport). Overrides " +
				"the provider default_options for this resource; an empty list sends no options.",
			Optional:    true,
			ElementType: types.StringType,
		},
	}
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
	if req.ProviderData == nil {
		return nil
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("expected *providerData, got %T; this is a provider bug", req.ProviderData))
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

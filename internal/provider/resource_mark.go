package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var markSpec = engineSpec{RestType: "marks", ShortName: "mark", CreateOptions: []string{"isImport"}}

// markState maps the midpoint_mark schema.
type markState struct {
	commonModel
	Name        types.String  `tfsdk:"name"`
	Description types.String  `tfsdk:"description"`
	Display     types.Dynamic `tfsdk:"display"`
	Event       types.Dynamic `tfsdk:"event"`
	Extra       types.Dynamic `tfsdk:"extra"`
}

func (s *markState) fields() simpleFields {
	return simpleFields{
		Strings: []stringField{
			{"name", "name", &s.Name},
			{"description", "description", &s.Description},
		},
		Dynamics: []dynamicField{
			{"display", "display", &s.Display},
			{"eventMark", "event", &s.Event},
		},
		Extra: &s.Extra,
	}
}

// NewMarkResource returns the midpoint_mark resource.
func NewMarkResource() resource.Resource {
	return simpleResource(markSpec.ShortName, markSpec, typedSchema(
		"A midPoint mark (MarkType): an event or object mark.",
		map[string]schema.Attribute{
			"name":        nameAttribute(),
			"description": optionalString("Description."),
			"display": optionalDynamic("Display properties (display) in midPoint naming, e.g. " +
				"{ label = \"Protected\", icon = { cssClass = \"fa fa-lock\" } }."),
			"event": optionalDynamic("Event mark information (eventMark) in midPoint naming, e.g. " +
				"{ enabledByDefault = true }."),
			"extra": extraAttribute(),
		}, nil),
		(*markState).fields)
}

package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

var serviceKind = focusKind{
	Name:        "service",
	Description: "A midPoint service (ServiceType): an application or system that roles grant access to.",
	Spec: engineSpec{
		RestType: "services", ShortName: "service",
		CreateOptions: []string{"isImport", "raw"}, UpdateOptions: []string{"raw"},
		MembersCheck: roleMembersCheck,
	},
	Authorization: true,
}

// NewServiceResource returns the midpoint_service resource.
func NewServiceResource() resource.Resource {
	return newFocusResource[roleLikeState](
		serviceKind,
		map[string]schema.Attribute{"authorization": authorizationAttribute()},
	)
}

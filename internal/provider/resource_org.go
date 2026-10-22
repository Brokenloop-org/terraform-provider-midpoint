package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var orgKind = focusKind{
	Name:        "org",
	Description: "A midPoint organizational unit (OrgType); a tenant is an org with tenant = true.",
	Spec: engineSpec{
		RestType: "orgs", ShortName: "org",
		CreateOptions: []string{"isImport"},
		MembersCheck:  roleMembersCheck,
	},
	Authorization: true,
	Tenant:        true,
}

type orgState struct {
	focusModel
	Authorization types.Dynamic `tfsdk:"authorization"`
	Tenant        types.Bool    `tfsdk:"tenant"`
}

func (s *orgState) model() focusModel {
	m := s.focusModel
	m.Authorization, m.Tenant = s.Authorization, s.Tenant
	return m
}

func (s *orgState) setModel(m focusModel) {
	s.focusModel = m
	s.Authorization, s.Tenant = m.Authorization, m.Tenant
}

// NewOrgResource returns the midpoint_org resource.
func NewOrgResource() resource.Resource {
	return &focusResource[orgState, *orgState]{
		kind: orgKind,
		attributes: map[string]schema.Attribute{
			"authorization": authorizationAttribute(),
			"tenant":        schema.BoolAttribute{Description: "Whether the org is a tenant.", Optional: true},
		},
	}
}

package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var roleKind = focusKind{
	Name: "role",
	Description: "A midPoint role (RoleType). Simple, business and service roles and metaroles are all roles; " +
		"the archetype tells them apart.",
	Spec: engineSpec{
		RestType: "roles", ShortName: "role",
		CreateOptions: []string{"isImport", "raw"}, UpdateOptions: []string{"raw"},
		MembersCheck: roleMembersCheck,
	},
	Authorization: true,
}

// roleLikeState is the state of midpoint_role and midpoint_service.
type roleLikeState struct {
	focusModel
	Authorization types.Dynamic `tfsdk:"authorization"`
}

func (s *roleLikeState) model() focusModel {
	m := s.focusModel
	m.Authorization = s.Authorization
	return m
}

func (s *roleLikeState) setModel(m focusModel) {
	s.focusModel = m
	s.Authorization = m.Authorization
}

// authorizationAttribute is the authorization attribute of service, role
// and org.
func authorizationAttribute() schema.Attribute {
	return schema.DynamicAttribute{
		Description: "Authorizations (authorization), a list in midPoint naming, e.g. " +
			"[{ action = [\"http://midpoint.evolveum.com/xml/ns/public/security/authorization-model-3#read\"] }].",
		Optional: true,
	}
}

// NewRoleResource returns the midpoint_role resource.
func NewRoleResource() resource.Resource {
	return &focusResource[roleLikeState, *roleLikeState]{
		kind:       roleKind,
		attributes: map[string]schema.Attribute{"authorization": authorizationAttribute()},
	}
}

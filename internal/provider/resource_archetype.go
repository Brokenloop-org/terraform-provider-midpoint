package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var archetypeKind = focusKind{
	Name:        "archetype",
	Description: "A midPoint archetype (ArchetypeType).",
	Spec: engineSpec{
		RestType: "archetypes", ShortName: "archetype",
		CreateOptions: []string{"isImport", "raw"}, UpdateOptions: []string{"raw"},
		MembersCheck: roleMembersCheck,
	},
	Authorization: true,
	Archetype:     true,
}

type archetypeState struct {
	focusModel
	Authorization   types.Dynamic `tfsdk:"authorization"`
	HolderType      types.List    `tfsdk:"holder_type"`
	ArchetypePolicy types.Dynamic `tfsdk:"archetype_policy"`
}

func (s *archetypeState) model() focusModel {
	m := s.focusModel
	m.Authorization, m.HolderType, m.ArchetypePolicy = s.Authorization, s.HolderType, s.ArchetypePolicy
	return m
}

func (s *archetypeState) setModel(m focusModel) {
	s.focusModel = m
	s.Authorization, s.HolderType, s.ArchetypePolicy = m.Authorization, m.HolderType, m.ArchetypePolicy
}

// NewArchetypeResource returns the midpoint_archetype resource.
func NewArchetypeResource() resource.Resource {
	return newFocusResource[archetypeState](archetypeKind, map[string]schema.Attribute{
		"authorization": authorizationAttribute(),
		"holder_type": schema.ListAttribute{
			Description: "Object types that may hold the archetype, e.g. [\"RoleType\"]; written as the " +
				"assignmentRelation assignment (holderType #RoleType).",
			Optional:    true,
			ElementType: types.StringType,
		},
		"archetype_policy": schema.DynamicAttribute{
			Description: "Archetype policy (archetypePolicy) in midPoint naming, e.g. { display = { label = \"Cluster\" } }.",
			Optional:    true,
		},
	})
}

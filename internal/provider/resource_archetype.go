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
	Archetype: true,
}

type archetypeState struct {
	focusModel
	HolderType      types.List    `tfsdk:"holder_type"`
	ArchetypePolicy types.Dynamic `tfsdk:"archetype_policy"`
}

func (s *archetypeState) model() focusModel {
	m := s.focusModel
	m.HolderType, m.ArchetypePolicy = s.HolderType, s.ArchetypePolicy
	return m
}

func (s *archetypeState) setModel(m focusModel) {
	s.focusModel = m
	s.HolderType, s.ArchetypePolicy = m.HolderType, m.ArchetypePolicy
}

// NewArchetypeResource returns the midpoint_archetype resource.
func NewArchetypeResource() resource.Resource {
	return &focusResource[archetypeState, *archetypeState]{
		kind: archetypeKind,
		attributes: map[string]schema.Attribute{
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
		},
	}
}

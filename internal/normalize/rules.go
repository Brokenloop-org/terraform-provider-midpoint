// Package normalize is the single place that knows which parts of a midPoint
// object are managed server-side and the canonical form in which a configured
// body and a server body are compared.
package normalize

import (
	"slices"
	"strings"
)

// Rule marks an item as server-managed: midPoint computes or stamps it, so it
// is never configuration and is dropped by Canonical.
//
// Path grammar: local item names separated by "/", anchored at the object
// root. A leading "**/" makes the rest of the pattern match at any depth (it
// matches the trailing segments of the item path). A "*" segment matches any
// single item name. Container ids and list indexes are never part of a path.
type Rule struct {
	Path string
	// Types are the REST collection types (e.g. "tasks") the rule applies
	// to; nil means all types.
	Types []string
	// Reason says why midPoint, not the user, owns the item.
	Reason string
}

// ServerManaged lists the items midPoint manages server-side. An entry here
// hides any difference in that item from the plan, so only items that are
// never configuration belong here.
var ServerManaged = []Rule{
	// Serialization annotations.
	{Path: "**/@ns", Reason: "JSON serialization namespace annotation; midPoint 4.9 emits it on the root and on every container value"},
	{Path: "@type", Reason: "root type annotation; the REST collection already determines the object type (nested @type is kept)"},
	{Path: "**/@incomplete", Reason: "marks a value the server did not fully return; never configuration"},

	// Metadata.
	{Path: "**/metadata", Reason: "MetadataType (legacy object/container metadata): create/modify timestamps, channels, actors"},
	{Path: "**/@metadata", Reason: "ValueMetadataType (storage, process, provenance) stamped on the object and on every container value"},
	{Path: "**/cachingMetadata", Reason: "CachingMetadataType: retrieval timestamp and serial number of cached data"},
	{Path: "credentials/*/metadata", Reason: "credential metadata (password change timestamps); also covered by **/metadata, listed for explicitness"},

	// Object bookkeeping.
	{Path: "version", Reason: "ObjectType.version: repository optimistic-locking counter"},
	{Path: "iteration", Reason: "FocusType/ShadowType iteration counter used for name uniqueness"},
	{Path: "iterationToken", Reason: "iteration token derived from iteration"},
	{Path: "operationExecution", Reason: "OperationExecutionType: record of the last operations executed on the object"},
	{Path: "fetchResult", Reason: "OperationResultType of the read operation"},
	{Path: "diagnosticInformation", Reason: "DiagnosticInformationType: diagnostic records added by the server"},
	{Path: "trigger", Reason: "TriggerType: scheduled internal actions created by the server (e.g. validity recompute)"},
	{Path: "behavior", Reason: "FocusType.behavior (BehaviorType): login/authentication statistics; ReportType.behavior is exempted in configurationOverrides"},
	{Path: "lensContext", Reason: "LensContextType: serialized model operation context"},

	// Computed references.
	{Path: "roleMembershipRef", Reason: "computed from assignments and inducements"},
	{Path: "archetypeRef", Reason: "computed from archetype assignments"},
	{Path: "linkRef", Reason: "links to shadows, maintained by the model"},
	{Path: "parentOrgRef", Reason: "computed from org assignments"},
	{Path: "tenantRef", Reason: "computed from tenant assignments"},
	{Path: "delegatedRef", Reason: "computed from deputy assignments"},
	{Path: "effectiveMarkRef", Reason: "computed from policy statements and mark rules"},
	{Path: "**/targetName", Reason: "ObjectReferenceType.targetName: display name resolved by the server"},

	// Policy evaluation results.
	{Path: "policySituation", Reason: "computed by policy rule evaluation"},
	{Path: "triggeredPolicyRule", Reason: "computed by policy rule evaluation"},

	// Computed activation. ActivationType is used by the focus and by every
	// assignment/inducement value (midPoint 4.9 adds effectiveStatus to each
	// assignment), so these match at any depth. The resource-side
	// ResourceActivationDefinitionType has none of these children.
	{Path: "**/activation/effectiveStatus", Reason: "computed from administrativeStatus and validity"},
	{Path: "**/activation/validityStatus", Reason: "computed from validFrom/validTo"},
	{Path: "**/activation/validityChangeTimestamp", Reason: "timestamp of the last computed validity change"},
	{Path: "**/activation/disableTimestamp", Reason: "timestamp of the last computed disable"},
	{Path: "**/activation/enableTimestamp", Reason: "timestamp of the last computed enable; 4.9 stamps it on every focus"},
	{Path: "**/activation/archiveTimestamp", Reason: "timestamp of the last computed archive"},
	{Path: "**/activation/disableReason", Reason: "computed reason of the effective disable"},

	// Resources.
	{Path: "operationalState", Types: []string{"resources"}, Reason: "OperationalStateType: last observed availability"},
	{Path: "operationalStateHistory", Types: []string{"resources"}, Reason: "history of observed availability"},
	{Path: "capabilities/native", Types: []string{"resources"}, Reason: "capabilities reported by the connector"},
	{Path: "capabilities/cachingMetadata", Types: []string{"resources"}, Reason: "cache stamp of the native capabilities"},
	{Path: "schema/cachingMetadata", Types: []string{"resources"}, Reason: "cache stamp of the fetched resource schema"},

	// Tasks: runtime state of the task manager and activity framework.
	{Path: "taskIdentifier", Types: []string{"tasks"}, Reason: "generated task identifier"},
	{Path: "executionState", Types: []string{"tasks"}, Reason: "runtime execution state (running, suspended, closed)"},
	{Path: "schedulingState", Types: []string{"tasks"}, Reason: "runtime scheduling state"},
	{Path: "stateBeforeSuspend", Types: []string{"tasks"}, Reason: "execution state saved on suspend"},
	{Path: "schedulingStateBeforeSuspend", Types: []string{"tasks"}, Reason: "scheduling state saved on suspend; seen in the recorded 4.9 task"},
	{Path: "node", Types: []string{"tasks"}, Reason: "cluster node the task runs on"},
	{Path: "nodeAsObserved", Types: []string{"tasks"}, Reason: "cluster node observed by the scheduler"},
	{Path: "lastRunStartTimestamp", Types: []string{"tasks"}, Reason: "runtime timestamp"},
	{Path: "lastRunFinishTimestamp", Types: []string{"tasks"}, Reason: "runtime timestamp"},
	{Path: "completionTimestamp", Types: []string{"tasks"}, Reason: "runtime timestamp"},
	{Path: "nextRunStartTimestamp", Types: []string{"tasks"}, Reason: "computed from the schedule"},
	{Path: "result", Types: []string{"tasks"}, Reason: "OperationResultType of the last run"},
	{Path: "resultStatus", Types: []string{"tasks"}, Reason: "status of the last run"},
	{Path: "progress", Types: []string{"tasks"}, Reason: "runtime progress counter"},
	{Path: "expectedTotal", Types: []string{"tasks"}, Reason: "runtime progress estimate"},
	{Path: "operationStats", Types: []string{"tasks"}, Reason: "OperationStatsType: runtime performance statistics"},
	{Path: "activityState", Types: []string{"tasks"}, Reason: "TaskActivityStateType: runtime activity state, buckets and statistics"},
	{Path: "affectedObjects", Types: []string{"tasks"}, Reason: "TaskAffectedObjectsType: derived from the activity definition"},
	{Path: "unpauseAction", Types: []string{"tasks"}, Reason: "set by the task manager when a task waits"},

	// Function libraries.
	{Path: "function/parameter/@type", Types: []string{"functionLibraries"}, Reason: "midPoint 4.9 annotates every function parameter value with its type c:ExpressionParameterType, which is the only type a parameter has; a serialization annotation, never configuration (recorded in testdata/real/function-library.json)"},
}

// configurationOverrides are configuration items whose path a broader
// ServerManaged rule would otherwise match. They win over ServerManaged.
var configurationOverrides = []Rule{
	{Path: "behavior", Types: []string{"reports"}, Reason: "ReportType.behavior (ReportBehaviorType: direction, import options) is configuration; the behavior rule targets FocusType.behavior"},
	{Path: "**/identityProvider/metadata", Reason: "Saml2ProviderAuthenticationModuleType.metadata (IdP metadata in a security policy SAML2 module) is configuration; **/metadata targets MetadataType"},
	{Path: "schema/definition/@ns", Types: []string{"resources"}, Reason: "the XML Schema namespace of a resource schema definition (SchemaDefinitionType) is part of the value: midPoint 4.9 rejects a definition written without it (\"Cannot parse schema\"), and a delta that replaces the definition carries the canonical value; **/@ns targets serialization annotations (recorded in testdata/real/resource-csv.json)"},
}

// Type QName values ("#UserType", "c:UserType", "UserType") are not a rule
// here: Canonical folds them in every value (see qnameValue in canonical.go),
// because midPoint returns "#UserType" for any QName item written as
// "UserType" and the set of QName-valued items is open-ended.

// MultiValued holds the local names of multi-valued items. Canonical wraps a
// single value of these items into a one-element list so that the JSON
// shape of one value and of several values compare alike.
var MultiValued = map[string]bool{
	"assignment":                 true, // AssignmentHolderType.assignment (AssignmentType)
	"inducement":                 true, // AbstractRoleType.inducement (AssignmentType)
	"authorization":              true, // AbstractRoleType.authorization (AuthorizationType)
	"action":                     true, // AuthorizationType.action (anyURI); LegacySynchronizationReactionType.action
	"object":                     true, // AuthorizationType.object (AuthorizationObjectSelectorType)
	"objectType":                 true, // SchemaHandlingType.objectType (ResourceObjectTypeDefinitionType)
	"attribute":                  true, // ResourceObjectTypeDefinitionType.attribute; ConstructionType.attribute
	"association":                true, // ResourceObjectTypeDefinitionType.association; ConstructionType.association
	"inbound":                    true, // ResourceItemDefinitionType.inbound (InboundMappingType)
	"outbound":                   true, // ResourceBidirectionalMappingType.outbound (activation/credentials mappings)
	"source":                     true, // AbstractMappingType.source (VariableBindingDefinitionType)
	"reaction":                   true, // SynchronizationReactionsType.reaction (SynchronizationReactionType)
	"mapping":                    true, // ObjectTemplateType.mapping; ObjectTemplateItemDefinitionType.mapping
	"item":                       true, // ObjectTemplateType.item (ObjectTemplateItemDefinitionType); AuthorizationType.item
	"objectClass":                true, // SchemaHandlingType.objectClass (ResourceObjectClassDefinitionType)
	"generateObjectClass":        true, // SchemaGenerationConstraintsType.generateObjectClass (QName)
	"holderType":                 true, // AssignmentRelationType.holderType (QName)
	"assignmentRelation":         true, // AssignmentType.assignmentRelation (AssignmentRelationType)
	"script":                     true, // OperationProvisioningScriptsType.script (OperationProvisioningScriptType)
	"const":                      true, // ExpressionType expression evaluator (ConstExpressionEvaluatorType), unbounded
	"value":                      true, // ExpressionType literal expression evaluator, unbounded
	"objectClassesToSynchronize": true, // ConnId LDAP connector configurationProperties (connector schema, unbounded)
	"operationalAttributes":      true, // ConnId LDAP connector configurationProperties (connector schema, unbounded)
	"allowedValues":              true, // connector configuration property restriction (connector schema, unbounded)
	"trigger":                    true, // ObjectType.trigger (TriggerType)
	"link":                       true, // SynchronizationActionsType.link (LinkSynchronizationActionType)
	"synchronize":                true, // SynchronizationActionsType.synchronize, unbounded; 4.9 returns one as an object (real/resource-csv-synchronization.json)
	"addFocus":                   true, // SynchronizationActionsType.addFocus, unbounded; 4.9 returns one as an object (real/resource-csv-synchronization.json)
	"items":                      true, // CorrelatorsType.items (ItemsCorrelatorType), unbounded; 4.9 returns one as an object (real/resource-csv-synchronization.json)
	"property":                   true, // PropertyReferenceListType.property (ItemPathType)
	"panel":                      true, // GuiObjectDetailsPageType.panel (ContainerPanelConfigurationType); archetype-application fixture
	"supportedTypes":             true, // ObjectTypeSearchItemConfigurationType.supportedTypes (QName); archetype-application fixture
	"function":                   true, // FunctionLibraryType.function (ExpressionType), unbounded; 4.9 returns a single function as an object
}

// IsServerManaged reports whether the item at path (local names only, no
// container ids, no list indexes) of an object of REST type restType is
// managed server-side.
func IsServerManaged(restType string, path []string) bool {
	if len(path) == 0 {
		return false
	}
	for _, r := range configurationOverrides {
		if r.matches(restType, path) {
			return false
		}
	}
	for _, r := range ServerManaged {
		if r.matches(restType, path) {
			return true
		}
	}
	return false
}

func (r Rule) matches(restType string, path []string) bool {
	if r.Types != nil && !slices.Contains(r.Types, restType) {
		return false
	}
	pattern := strings.Split(r.Path, "/")
	if pattern[0] == "**" {
		pattern = pattern[1:]
		if len(path) < len(pattern) {
			return false
		}
		path = path[len(path)-len(pattern):]
	} else if len(path) != len(pattern) {
		return false
	}
	for i, seg := range pattern {
		if seg != "*" && seg != path[i] {
			return false
		}
	}
	return true
}

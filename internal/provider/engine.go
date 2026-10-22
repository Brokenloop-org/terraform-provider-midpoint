package provider

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/jplana/terraform-provider-midpoint/internal/client"
	"github.com/jplana/terraform-provider-midpoint/internal/delta"
	"github.com/jplana/terraform-provider-midpoint/internal/normalize"
)

// engineSpec describes one midPoint object type to the CRUD engine.
type engineSpec struct {
	// RestType is the REST collection type, e.g. "roles".
	RestType string
	// ShortName is the key into the provider's default_options, e.g. "role".
	ShortName string
	// CreateOptions and UpdateOptions are the built-in execution options,
	// used when neither the resource nor default_options set any.
	CreateOptions, UpdateOptions []string
	// ReadOptions are sent on every read, e.g. ["noFetch"] for resources.
	ReadOptions []string
	// MembersCheck returns the REST type and Axiom filter of a search for
	// objects that still reference oid; a delete is refused while it finds
	// any, unless forced. nil means no check. Use roleMembersCheck or
	// shadowsCheck.
	MembersCheck func(oid string) (restType, axiomFilter string)
}

// objectState is what a resource hands the engine to create an object.
type objectState struct {
	// OID is the configured oid; empty lets midPoint generate one.
	OID           string
	AdoptExisting bool
	ForceDestroy  bool
	// Options is the resource's options attribute: nil when unset, an empty
	// slice for an explicitly empty list (send no options).
	Options []string
	// Body is the desired object body without the type wrapper.
	Body map[string]any
}

// engine runs create, read, update and delete for one object type and
// enforces the safety rules: no overwrite of an existing object (unless
// adopted), narrow item deltas only, and no delete of a still-referenced
// object unless forced.
//
// The engine never logs or reports object bodies or delta values: they can
// hold secrets.
type engine struct {
	c        *client.Client
	defaults map[string][]string
	spec     engineSpec
	// observe, when set, receives the canonical body of every successful
	// readInto (refresh, import and the read-back after a write), whether
	// or not it differs from the desired body. Resources use it for
	// computed attributes, e.g. a resource's connector_oid.
	observe func(actual map[string]any)
}

// newEngine returns the engine for spec using the configured provider.
func newEngine(data *providerData, spec engineSpec) *engine {
	return &engine{c: data.Client, defaults: data.DefaultOptions, spec: spec}
}

// roleMembersCheck finds the objects assigned to oid or holding it in
// roleMembershipRef (members of a role, org, service or archetype).
// AssignmentHolderType is the most general type that defines both items;
// midPoint 4.9 rejects the filter on "objects".
func roleMembersCheck(oid string) (restType, axiomFilter string) {
	q := axiomString(oid)
	return "assignmentHolders", "roleMembershipRef matches (oid = " + q + ") or assignment/targetRef matches (oid = " + q + ")"
}

// shadowsCheck finds the shadows of resource oid.
func shadowsCheck(oid string) (restType, axiomFilter string) {
	return "shadows", "resourceRef matches (oid = " + axiomString(oid) + ")"
}

// axiomString quotes s as an Axiom string literal.
func axiomString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (e *engine) ref(oid string) string {
	return e.spec.RestType + "/" + oid
}

func (e *engine) getOptions() client.GetOptions {
	return client.GetOptions{Options: e.spec.ReadOptions, Exclude: readExcludes}
}

// create adds the object described by st. With an OID (st.OID, or an "oid"
// in st.Body when st.OID is empty) it first reads that OID: when the object
// exists and st.AdoptExisting is false it fails without writing anything;
// when it exists and st.AdoptExisting is true it returns adopted == true
// without writing (the caller then runs update). Otherwise it POSTs the body
// with empty items pruned and the oid in the body, using the effective
// create options plus reevaluateSearchFilters when the body holds a
// filter-only reference, and returns the oid midPoint kept or assigned.
func (e *engine) create(ctx context.Context, st objectState) (oid string, adopted bool, diags diag.Diagnostics) {
	body := pruneEmpty(st.Body)
	oid = st.OID
	if oid == "" {
		oid, _ = body["oid"].(string)
	}
	if oid != "" {
		_, err := e.c.Get(ctx, e.spec.RestType, oid, e.getOptions())
		switch {
		case err == nil && !st.AdoptExisting:
			diags.AddError("Object already exists",
				fmt.Sprintf("object %s already exists; import it or set adopt_existing = true", e.ref(oid)))
			return "", false, diags
		case err == nil:
			return oid, true, diags
		case !errors.Is(err, client.ErrNotFound):
			diags.AddError("Cannot check for an existing object",
				fmt.Sprintf("reading %s before creating it: %s", e.ref(oid), err))
			return "", false, diags
		}
		body["oid"] = oid
	}

	options := effectiveOptions(e.defaults, e.spec.ShortName, st.Options, e.spec.CreateOptions)
	if hasFilterOnlyReference(body) {
		options = withOption(options, optionReevaluateSearchFilters)
	}
	tflog.Debug(ctx, "creating midPoint object", map[string]any{"type": e.spec.RestType, "oid": oid, "options": options})
	created, err := e.c.Create(ctx, client.Object{Type: e.spec.RestType, Body: body}, options)
	if err != nil {
		diags.AddError("Cannot create object", fmt.Sprintf("creating %s: %s", e.spec.RestType, err))
		return "", false, diags
	}
	return created, false, diags
}

// hasFilterOnlyReference reports whether body holds a reference with a
// filter and no oid, using the same detection as for update deltas.
func hasFilterOnlyReference(body map[string]any) bool {
	adds := make([]client.ItemDelta, 0, len(body))
	for name, v := range body {
		adds = append(adds, client.ItemDelta{ModificationType: "add", Path: name, Value: v})
	}
	return delta.NeedsFilterReevaluation(adds)
}

// read returns the canonical body (normalize.Canonical) of the object oid.
// found is false when midPoint answers 404 (no diagnostic) and when the read
// fails (an error diagnostic); check diags first.
func (e *engine) read(ctx context.Context, oid string) (actual map[string]any, found bool, diags diag.Diagnostics) {
	obj, err := e.c.Get(ctx, e.spec.RestType, oid, e.getOptions())
	if errors.Is(err, client.ErrNotFound) {
		return nil, false, diags
	}
	if err != nil {
		diags.AddError("Cannot read object", fmt.Sprintf("reading %s: %s", e.ref(oid), err))
		return nil, false, diags
	}
	return normalize.Canonical(e.spec.RestType, obj.Body), true, diags
}

// canonicalPair returns the canonical forms of desired (empty maps, lists
// and nulls pruned first: they never belong in a desired body) and actual.
func (e *engine) canonicalPair(desired, actual map[string]any) (d, a map[string]any) {
	return normalize.Canonical(e.spec.RestType, pruneEmpty(desired)), normalize.Canonical(e.spec.RestType, actual)
}

// reconcile compares desired with actual (either raw or canonical; both are
// canonicalized) and returns whether they are equal and, when not, the item
// deltas that turn actual into desired. err is set when the difference
// cannot be expressed as item deltas. ctx is reserved for logging.
func (e *engine) reconcile(_ context.Context, desired, actual map[string]any) (equal bool, deltas []client.ItemDelta, err error) {
	cd, ca := e.canonicalPair(desired, actual)
	d, a := delta.AlignForType(e.spec.RestType, cd, ca)
	if normalize.Equal(d, a) {
		return true, nil, nil
	}
	// Compute aligns again without the type; the type-aligned pair keeps
	// it from deleting items compared only when configured for the type.
	deltas, err = delta.Compute(d, a)
	return false, deltas, err
}

// update reads the object oid and, when it differs from desired, sends one
// PATCH with the item deltas, using the effective update options (options is
// the resource attribute, nil when unset) plus reevaluateSearchFilters when a
// delta adds a filter-only reference. Nothing is sent when they are equal.
func (e *engine) update(ctx context.Context, oid string, desired map[string]any, options []string) diag.Diagnostics {
	actual, found, diags := e.read(ctx, oid)
	if diags.HasError() {
		return diags
	}
	if !found {
		diags.AddError("Object not found", fmt.Sprintf("cannot update %s: the object no longer exists in midPoint", e.ref(oid)))
		return diags
	}
	equal, deltas, err := e.reconcile(ctx, desired, actual)
	if err != nil {
		diags.AddError("Cannot compute the changes", fmt.Sprintf("updating %s: %s", e.ref(oid), err))
		return diags
	}
	if equal || len(deltas) == 0 {
		return diags
	}

	opts := effectiveOptions(e.defaults, e.spec.ShortName, options, e.spec.UpdateOptions)
	if delta.NeedsFilterReevaluation(deltas) {
		opts = withOption(opts, optionReevaluateSearchFilters)
	}
	paths := make([]string, len(deltas))
	for i, d := range deltas {
		paths[i] = d.ModificationType + " " + d.Path
	}
	tflog.Debug(ctx, "modifying midPoint object", map[string]any{"type": e.spec.RestType, "oid": oid, "deltas": paths, "options": opts})
	if err := e.c.Modify(ctx, e.spec.RestType, oid, deltas, opts); err != nil {
		diags.AddError("Cannot update object", fmt.Sprintf("updating %s: %s", e.ref(oid), err))
	}
	return diags
}

// delete removes the object oid. Unless force is set, a spec with a
// MembersCheck first searches (raw, at most membersSearchLimit results) for
// objects that still reference it and refuses while any exist. An object
// that is already gone is a success.
func (e *engine) delete(ctx context.Context, oid string, force bool) diag.Diagnostics {
	var diags diag.Diagnostics
	if e.spec.MembersCheck != nil && !force {
		typ, filter := e.spec.MembersCheck(oid)
		objs, total, err := e.c.Search(ctx, typ, filter, membersSearchLimit, membersSearchOptions)
		if err != nil {
			diags.AddError("Cannot check for references",
				fmt.Sprintf("cannot delete %s: searching for objects that still reference it: %s", e.ref(oid), err))
			return diags
		}
		if total > 0 || len(objs) > 0 {
			diags.AddError("Object still referenced", membersMessage(e.ref(oid), objs, total))
			return diags
		}
	}
	if err := e.c.Delete(ctx, e.spec.RestType, oid, nil); err != nil && !errors.Is(err, client.ErrNotFound) {
		diags.AddError("Cannot delete object", fmt.Sprintf("deleting %s: %s", e.ref(oid), err))
	}
	return diags
}

// membersMessage explains a refused delete. total is the server's count, or
// -1 when it reported none (then len(objs) is a lower bound).
func membersMessage(ref string, objs []client.Object, total int) string {
	count := fmt.Sprintf("%d objects", max(total, len(objs)))
	if total < 0 {
		count = fmt.Sprintf("at least %d objects", len(objs))
	}
	first := ""
	if len(objs) > 0 {
		names := make([]string, len(objs))
		for i, o := range objs {
			names[i] = objectName(o)
		}
		first = " (first: " + strings.Join(names, ", ") + ")"
	}
	return fmt.Sprintf("cannot delete %s: %s still reference it%s; set force_destroy = true to delete anyway", ref, count, first)
}

// objectName is an object's name (string or polystring), else its oid.
func objectName(o client.Object) string {
	switch n := o.Body["name"].(type) {
	case string:
		return n
	case map[string]any:
		if s, ok := n["orig"].(string); ok {
			return s
		}
	}
	return o.OID
}

// readInto reads the object oid and compares it with desired. found is false
// when the object is gone (the resource removes itself from state) or the
// read failed (an error diagnostic; check diags first). When they are equal
// nothing else happens. When they differ:
//
//   - afterWrite (right after create or update): a warning names the
//     differing top-level items, which midPoint normalized differently from
//     the configuration; setFromActual is not called, so the state keeps
//     the configured values and the next plan shows the difference;
//   - otherwise (refresh, import): setFromActual receives the canonical
//     actual body so the resource can surface the drift.
func (e *engine) readInto(ctx context.Context, oid string, desired map[string]any, afterWrite bool, setFromActual func(actual map[string]any)) (found bool, diags diag.Diagnostics) {
	actual, found, diags := e.read(ctx, oid)
	if diags.HasError() || !found {
		return found, diags
	}
	if e.observe != nil {
		e.observe(actual)
	}
	cd, ca := e.canonicalPair(desired, actual)
	d, a := delta.AlignForType(e.spec.RestType, cd, ca)
	if normalize.Equal(d, a) {
		return true, diags
	}
	if afterWrite {
		items := "the object"
		if names := differingItems(d, a); len(names) > 0 {
			items = strings.Join(names, ", ")
		}
		diags.AddWarning("midPoint changed the applied configuration",
			fmt.Sprintf("midPoint normalized %s; the next plan will show this difference", items))
		return true, diags
	}
	setFromActual(actual)
	return true, diags
}

// readAfterWrite reads the object oid back right after a write and compares
// it with desired, keeping the configured state (readInto with afterWrite;
// a difference is the readInto warning).
//
// created says the write was a create or an adoption and the resource has
// already recorded the object in state. A read-back failure (transport or
// API error, or the object not found) is then a warning: an error would
// taint the object, and the next apply would replace it, deleting an object
// that was written successfully (or a built-in that was adopted). The next
// refresh reconciles the state. After an update (created false) the
// failures stay errors; a failed update does not taint.
func (e *engine) readAfterWrite(ctx context.Context, oid string, desired map[string]any, created bool) diag.Diagnostics {
	found, diags := e.readInto(ctx, oid, desired, true, func(map[string]any) {})
	if !found && !diags.HasError() {
		diags.AddError("Object not found after write",
			fmt.Sprintf("%s was written but cannot be read back", e.ref(oid)))
	}
	if !created || !diags.HasError() {
		return diags
	}
	var out diag.Diagnostics
	for _, d := range diags {
		if d.Severity() != diag.SeverityError {
			out.Append(d)
			continue
		}
		out.AddWarning(d.Summary(), fmt.Sprintf("%s. %s was written and is kept in state with the configured values; the next refresh reconciles it with midPoint.",
			strings.TrimSuffix(d.Detail(), "."), e.ref(oid)))
	}
	return out
}

// differingItems lists, sorted, the top-level items (not "@" annotations)
// whose values differ between two aligned canonical bodies.
func differingItems(d, a map[string]any) []string {
	var names []string
	for k, v := range d {
		if !strings.HasPrefix(k, "@") && !normalize.Equal(v, a[k]) {
			names = append(names, k)
		}
	}
	for k, v := range a {
		if _, seen := d[k]; !seen && !strings.HasPrefix(k, "@") && !normalize.Equal(nil, v) {
			names = append(names, k)
		}
	}
	slices.Sort(names)
	return names
}

// pruneEmpty returns a copy of m without nulls, empty maps and empty lists,
// recursively (a map or list that becomes empty is removed too). A desired
// body never holds empty items: delta.Compute sends nothing for an empty
// desired list against a missing actual one while normalize.Equal reports
// them unequal. A nil m yields an empty map.
func pruneEmpty(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if pv, keep := pruneValue(v); keep {
			out[k] = pv
		}
	}
	return out
}

func pruneValue(v any) (any, bool) {
	switch t := v.(type) {
	case nil:
		return nil, false
	case map[string]any:
		p := pruneEmpty(t)
		return p, len(p) > 0
	case []any:
		out := make([]any, 0, len(t))
		for _, e := range t {
			if pe, keep := pruneValue(e); keep {
				out = append(out, pe)
			}
		}
		return out, len(out) > 0
	default:
		return v, true
	}
}

// adoptionMaxChanges caps the changes the adoption warning lists.
const adoptionMaxChanges = 20

// adoptionWarning is the detail of the plan-time warning for adopting the
// existing object oid, read as actual, with the configured body desired
// (nil when it is not known at plan time). It lists the item deltas the
// adoption's update will send, one "<modificationType> <path>" line each,
// without values: they can hold secrets. A replace without a value removes
// the item and says so. When the changes cannot be computed it falls back to
// a generic sentence.
func (e *engine) adoptionWarning(ctx context.Context, address, oid string, desired, actual map[string]any) string {
	lead := fmt.Sprintf("%s %s exists and will be adopted", address, e.ref(oid))
	if desired == nil {
		return lead + "; the configured content is applied as an update. Some configured values are known " +
			"only during apply, so the changes cannot be listed; items the configuration leaves out are removed."
	}
	equal, deltas, err := e.reconcile(ctx, desired, actual)
	if err != nil {
		return lead + "; the configured content is applied as an update"
	}
	if equal || len(deltas) == 0 {
		return lead + "; applying the configuration will send no changes."
	}
	lines := make([]string, 0, min(len(deltas), adoptionMaxChanges)+1)
	for i, d := range deltas {
		if i == adoptionMaxChanges {
			lines = append(lines, fmt.Sprintf("and %d more", len(deltas)-adoptionMaxChanges))
			break
		}
		line := d.ModificationType + " " + d.Path
		if d.ModificationType == "replace" && d.Value == nil {
			line += " (removes it)"
		}
		lines = append(lines, "  "+line)
	}
	return lead + "; applying the configuration will send these changes:\n" + strings.Join(lines, "\n")
}

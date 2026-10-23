package convert

import (
	"regexp"
	"strings"
)

// resource projects a midPoint resource. It inverts the provider's
// resourceToBody (internal/provider/resource_resource.go).
func (p *projector) resource() {
	p.takeAll(simpleMap{
		strings: map[string]string{"name": "name", "description": "description", "lifecycleState": "lifecycle_state"},
		dynamics: map[string]string{
			"connectorConfiguration": "connector_configuration",
			"schemaHandling":         "schema_handling",
			"synchronization":        "synchronization",
		},
	})
	if hasKey(p.r.Attrs["connector_configuration"], "clearValue") {
		p.note("connector_configuration holds a clearValue (a clear-text secret); it will be in Terraform state")
	}

	if ref, ok := p.rest["connectorRef"].(map[string]any); ok {
		if typ, version, ok := connectorFromRef(ref); ok {
			c := map[string]any{"type": typ}
			if version != "" {
				c["version"] = version
			}
			p.r.Attrs["connector"] = c
			delete(p.rest, "connectorRef")
			if _, hasOID := ref["oid"]; hasOID {
				p.note("connectorRef: oid dropped, midPoint resolves the connector from type and version")
			}
		} else {
			p.note("connectorRef: not a filter on connectorType (and connectorVersion), kept in extra; connector must be set by hand")
		}
	}

	// capabilities is capabilities/configured; native is server-managed.
	if caps, ok := p.rest["capabilities"].(map[string]any); ok {
		if cfg, ok := caps["configured"]; ok && cfg != nil {
			p.r.Attrs["capabilities"] = cfg
			delete(caps, "configured")
		}
		if len(caps) == 0 {
			delete(p.rest, "capabilities")
		} else {
			p.note("capabilities: items other than configured are not expressible and were dropped: %s", strings.Join(sortedKeys(caps), ", "))
			delete(p.rest, "capabilities")
		}
	}

	// schema { generation_constraints, definition }; definition is the
	// xsd text in schema/definition/schema.
	if sch, ok := p.rest["schema"].(map[string]any); ok {
		out := map[string]any{}
		if gc, ok := sch["generationConstraints"]; ok && gc != nil {
			out["generation_constraints"] = gc
			delete(sch, "generationConstraints")
		}
		if def, ok := sch["definition"].(map[string]any); ok {
			if text, ok := def["schema"].(string); ok {
				out["definition"] = text
				delete(def, "schema")
				delete(def, "@ns")
				if len(def) == 0 {
					delete(sch, "definition")
				}
			}
		}
		if len(out) > 0 {
			p.r.Attrs["schema"] = out
		}
		if len(sch) > 0 {
			p.note("schema: items other than generationConstraints and definition are not expressible and were dropped: %s", strings.Join(sortedKeys(sch), ", "))
		}
		delete(p.rest, "schema")
	}
}

// connectorFromRef reads the connector type and version from the filter of
// connectorRef, either structured (equal on connectorType and
// connectorVersion, alone or under and) or in the text query form.
func connectorFromRef(ref map[string]any) (typ, version string, ok bool) {
	for k := range ref {
		switch k {
		case "filter", "type", "oid", "relation":
		default:
			return "", "", false
		}
	}
	filter, _ := ref["filter"].(map[string]any)
	if filter == nil {
		return "", "", false
	}
	var conds map[string]string
	if text, isText := filter["text"].(string); isText && len(filter) == 1 {
		conds, ok = connectorTextConds(text)
	} else {
		conds, ok = connectorStructConds(filter)
	}
	if !ok || conds["connectorType"] == "" {
		return "", "", false
	}
	return conds["connectorType"], conds["connectorVersion"], true
}

var connectorCond = regexp.MustCompile(`^\s*(connectorType|connectorVersion)\s*=\s*"((?:[^"\\]|\\.)*)"\s*`)

// connectorTextConds parses `connectorType = "x" and connectorVersion = "y"`
// (either order, version optional).
func connectorTextConds(text string) (map[string]string, bool) {
	conds := map[string]string{}
	for {
		m := connectorCond.FindStringSubmatch(text)
		if m == nil {
			return nil, false
		}
		if _, dup := conds[m[1]]; dup {
			return nil, false
		}
		conds[m[1]] = strings.NewReplacer(`\\`, `\`, `\"`, `"`).Replace(m[2])
		text = text[len(m[0]):]
		if text == "" {
			return conds, true
		}
		rest, found := strings.CutPrefix(text, "and")
		if !found || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
			return nil, false
		}
		text = rest
	}
}

// connectorStructConds collects the equal conditions of a structured
// filter: equal alone, or any number of them under and.
func connectorStructConds(filter map[string]any) (map[string]string, bool) {
	conds := map[string]string{}
	var walk func(node map[string]any, top bool) bool
	walk = func(node map[string]any, top bool) bool {
		for k, v := range node {
			for _, child := range asList(v) {
				cm, ok := child.(map[string]any)
				if !ok {
					return false
				}
				switch k {
				case "and":
					if !walk(cm, false) {
						return false
					}
				case "equal":
					path, _ := cm["path"].(string)
					vals := asList(cm["value"])
					if len(cm) != 2 || len(vals) != 1 {
						return false
					}
					val, isStr := vals[0].(string)
					if !isStr || (path != "connectorType" && path != "connectorVersion") {
						return false
					}
					if _, dup := conds[path]; dup {
						return false
					}
					conds[path] = val
				default:
					return false
				}
			}
		}
		return true
	}
	return conds, walk(filter, true) && len(conds) > 0
}

// hasKey reports whether a map anywhere in v has key.
func hasKey(v any, key string) bool {
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t[key]; ok {
			return true
		}
		for _, e := range t {
			if hasKey(e, key) {
				return true
			}
		}
	case []any:
		for _, e := range t {
			if hasKey(e, key) {
				return true
			}
		}
	}
	return false
}

// task projects a task. It inverts the provider's taskToBody
// (internal/provider/resource_task.go).
func (p *projector) task() {
	p.takeAll(simpleMap{
		strings: map[string]string{
			"name": "name", "description": "description", "binding": "binding", "recurrence": "recurrence",
		},
		dynamics: map[string]string{"schedule": "schedule", "activity": "activity"},
	})

	// initial_execution_state is written on create only; the loader drops
	// executionState and schedulingState as server-managed, so this is
	// suspended unless the source still holds a runnable state.
	state := "suspended"
	if p.rest["executionState"] == "runnable" || p.rest["schedulingState"] == "ready" {
		state = "runnable"
	}
	p.r.Attrs["initial_execution_state"] = state
	delete(p.rest, "executionState")
	delete(p.rest, "schedulingState")

	if ref, ok := p.rest["ownerRef"].(map[string]any); ok {
		oid, _ := ref["oid"].(string)
		typ, hasType := ref["type"].(string)
		plain := oid != "" && (!hasType || typ == "UserType")
		for k, v := range ref {
			switch k {
			case "oid", "type":
			case "relation":
				plain = plain && v == defaultRelation
			default:
				plain = false
			}
		}
		if plain {
			p.r.Attrs["owner"] = oid
			delete(p.rest, "ownerRef")
		} else {
			p.note("ownerRef: not a plain user reference by oid, kept in extra")
		}
	}
}

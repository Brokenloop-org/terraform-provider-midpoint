// Package mpstub is an in-memory fake of the midPoint 4.9 REST API for fast
// tests without Docker. It stores objects as JSON bodies, answers reads with
// the server-managed noise a real midPoint adds (so normalization is
// exercised), applies item deltas, and understands exactly the search
// filters the provider's CRUD engine emits. It is a test helper: no
// persistence, no authentication, no schema.
package mpstub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/jplana/terraform-provider-midpoint/internal/client"
)

// restPath is the REST base path the client appends to the server URL.
const restPath = "/midpoint/ws/rest"

const (
	typesNS     = "http://prism.evolveum.com/xml/ns/public/types-3"
	commonNS    = "http://midpoint.evolveum.com/xml/ns/public/common/common-3"
	apiTypesNS  = "http://midpoint.evolveum.com/xml/ns/public/common/api-types-3"
	stampTime   = "2026-10-22T12:00:00.000Z"
	nodeVersion = "4.9.9"
)

// focusTypes get a computed root activation, like FocusType objects do.
var focusTypes = []string{"users", "roles", "orgs", "services", "archetypes"}

// The only search filters the stub understands: the members checks the
// engine runs before a delete.
var (
	membersFilter = regexp.MustCompile(`^roleMembershipRef matches \(oid = "([^"]*)"\) or assignment/targetRef matches \(oid = "([^"]*)"\)$`)
	shadowsFilter = regexp.MustCompile(`^resourceRef matches \(oid = "([^"]*)"\)$`)
)

// Recorded is one request the stub received.
type Recorded struct {
	Method string
	// Path is below the REST base path, e.g. "/roles/<oid>".
	Path  string
	Query url.Values
	// Body is the decoded JSON request body (numbers as json.Number), nil
	// when the request had none.
	Body map[string]any
}

type key struct{ typ, oid string }

type entry struct {
	body    map[string]any
	version int
}

// Server is a running stub.
type Server struct {
	ts *httptest.Server

	mu           sync.Mutex
	objects      map[key]*entry
	order        []key // insertion order, for deterministic search results
	requests     []Recorded
	reportTotals bool
	seq          int
}

// New starts a stub. Point a client.Config.URL at URL() and Close it when
// done.
func New() *Server {
	s := &Server{objects: map[key]*entry{}}
	s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

// URL is the server URL (without the REST base path).
func (s *Server) URL() string { return s.ts.URL }

// Close shuts the server down.
func (s *Server) Close() { s.ts.Close() }

// Put stores body (without the type wrapper) as an object of REST type
// restType, replacing any object with the same oid, and returns its oid. A
// missing oid is generated. Container values get an "@id" when they have
// none. Put is not recorded as a request.
func (s *Server) Put(restType string, body map[string]any) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store(restType, clone(body).(map[string]any))
}

// Get returns a copy of the stored body of restType/oid (without the noise
// a GET adds), or nil.
func (s *Server) Get(restType, oid string) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.objects[key{restType, oid}]
	if !ok {
		return nil
	}
	return clone(e.body).(map[string]any)
}

// Requests returns the requests received so far, in order.
func (s *Server) Requests() []Recorded {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

// ReportTotals makes searches send X-Total-Count. midPoint 4.9 never does,
// so the default is off.
func (s *Server) ReportTotals(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reportTotals = on
}

func (s *Server) store(typ string, body map[string]any) string {
	oid, _ := body["oid"].(string)
	if oid == "" {
		s.seq++
		oid = fmt.Sprintf("5ab00000-0000-4000-8000-%012d", s.seq)
		body["oid"] = oid
	}
	assignIDs(body)
	k := key{typ, oid}
	if e, ok := s.objects[k]; ok {
		e.body = body
		e.version++
		return oid
	}
	s.objects[k] = &entry{body: body, version: 1}
	s.order = append(s.order, k)
	return oid
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "reading request: "+err.Error())
		return
	}
	var body map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := decode(raw, &body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	}
	rel, ok := strings.CutPrefix(r.URL.Path, restPath)
	if !ok {
		writeError(w, http.StatusNotFound, "not a REST path: "+r.URL.Path)
		return
	}
	s.requests = append(s.requests, Recorded{Method: r.Method, Path: rel, Query: r.URL.Query(), Body: body})

	seg := strings.Split(strings.Trim(rel, "/"), "/")
	switch {
	case r.Method == http.MethodGet && len(seg) == 2 && seg[0] == "nodes" && seg[1] == "current":
		writeJSON(w, http.StatusOK, map[string]any{"node": map[string]any{"build": map[string]any{"version": nodeVersion}}})
	case r.Method == http.MethodGet && len(seg) == 2:
		s.get(w, seg[0], seg[1])
	case r.Method == http.MethodPost && len(seg) == 1:
		s.create(w, r, seg[0], body)
	case r.Method == http.MethodPost && len(seg) == 2 && seg[1] == "search":
		s.search(w, body)
	case r.Method == http.MethodPatch && len(seg) == 2:
		s.modify(w, seg[0], seg[1], body)
	case r.Method == http.MethodDelete && len(seg) == 2:
		s.delete(w, seg[0], seg[1])
	default:
		writeError(w, http.StatusMethodNotAllowed, fmt.Sprintf("stub does not support %s %s", r.Method, rel))
	}
}

func (s *Server) get(w http.ResponseWriter, typ, oid string) {
	e, ok := s.objects[key{typ, oid}]
	if !ok {
		writeNotFound(w, typ, oid)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{client.SingularOf(typ): s.view(typ, e)})
}

func (s *Server) create(w http.ResponseWriter, r *http.Request, typ string, body map[string]any) {
	if len(body) != 1 {
		writeError(w, http.StatusBadRequest, "expected one wrapped object")
		return
	}
	var obj map[string]any
	for _, v := range body {
		obj, _ = v.(map[string]any)
	}
	if obj == nil {
		writeError(w, http.StatusBadRequest, "expected one wrapped object")
		return
	}
	if oid, _ := obj["oid"].(string); oid != "" {
		if _, exists := s.objects[key{typ, oid}]; exists && !slices.Contains(r.URL.Query()["options"], "overwrite") {
			writeError(w, http.StatusConflict, fmt.Sprintf("Object with OID %s already exists", oid))
			return
		}
	}
	oid := s.store(typ, obj)
	w.Header().Set("Location", s.ts.URL+restPath+"/"+typ+"/"+oid)
	status := http.StatusCreated
	if typ == "tasks" {
		status = http.StatusAccepted
	}
	w.WriteHeader(status)
}

func (s *Server) modify(w http.ResponseWriter, typ, oid string, body map[string]any) {
	e, ok := s.objects[key{typ, oid}]
	if !ok {
		writeNotFound(w, typ, oid)
		return
	}
	mod, _ := body["objectModification"].(map[string]any)
	if mod == nil {
		writeError(w, http.StatusBadRequest, "expected objectModification")
		return
	}
	deltas := toList(mod["itemDelta"])
	// Apply to a copy so that a failing delta changes nothing.
	updated := clone(e.body).(map[string]any)
	for _, d := range deltas {
		dm, _ := d.(map[string]any)
		mt, _ := dm["modificationType"].(string)
		path, _ := dm["path"].(string)
		if err := applyDelta(updated, mt, path, dm["value"]); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("cannot apply %s of %s: %v", mt, path, err))
			return
		}
	}
	assignIDs(updated)
	e.body = updated
	e.version++
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) delete(w http.ResponseWriter, typ, oid string) {
	k := key{typ, oid}
	if _, ok := s.objects[k]; !ok {
		writeNotFound(w, typ, oid)
		return
	}
	delete(s.objects, k)
	s.order = slices.DeleteFunc(s.order, func(o key) bool { return o == k })
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) search(w http.ResponseWriter, body map[string]any) {
	query, _ := body["query"].(map[string]any)
	filter, _ := query["filter"].(map[string]any)
	text, _ := filter["text"].(string)
	maxSize := -1
	if paging, ok := query["paging"].(map[string]any); ok {
		if n, err := strconv.Atoi(fmt.Sprint(paging["maxSize"])); err == nil {
			maxSize = n
		}
	}

	var match func(k key, view map[string]any) bool
	if m := membersFilter.FindStringSubmatch(text); m != nil && m[1] == m[2] {
		target := m[1]
		match = func(_ key, v map[string]any) bool {
			return hasRef(v["roleMembershipRef"], target) || hasAssignmentTo(v["assignment"], target)
		}
	} else if m := shadowsFilter.FindStringSubmatch(text); m != nil {
		target := m[1]
		match = func(k key, v map[string]any) bool {
			return k.typ == "shadows" && hasRef(v["resourceRef"], target)
		}
	} else {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("stub does not understand the filter %q", text))
		return
	}

	var found []any
	total := 0
	for _, k := range s.order {
		v := s.view(k.typ, s.objects[k])
		if !match(k, v) {
			continue
		}
		total++
		if maxSize < 0 || len(found) < maxSize {
			v["@ns"] = commonNS
			v["@type"] = "c:" + typeName(k.typ)
			found = append(found, v)
		}
	}
	list := map[string]any{"@ns": apiTypesNS, "@type": apiTypesNS + "#ObjectListType"}
	if len(found) > 0 {
		list["object"] = found
	}
	if s.reportTotals {
		w.Header().Set("X-Total-Count", strconv.Itoa(total))
	}
	writeJSON(w, http.StatusOK, map[string]any{"@ns": typesNS, "object": list})
}

// view is the body a GET returns: the stored body plus the server-managed
// noise of the recorded midPoint 4.9 fixtures.
func (s *Server) view(typ string, e *entry) map[string]any {
	b := clone(e.body).(map[string]any)

	// Computed references from archetype assignments, built before the
	// stored references are annotated.
	var archetypes []string
	for _, a := range toList(b["assignment"]) {
		am, _ := a.(map[string]any)
		ref, _ := am["targetRef"].(map[string]any)
		if t, _ := ref["type"].(string); strings.TrimPrefix(t, "c:") == "ArchetypeType" {
			if oid, ok := ref["oid"].(string); ok {
				archetypes = append(archetypes, oid)
			}
		}
	}

	s.annotateRefs(b)

	for _, a := range toList(b["assignment"]) {
		am, ok := a.(map[string]any)
		if !ok {
			continue
		}
		act, ok := am["activation"].(map[string]any)
		if !ok {
			act = map[string]any{}
			am["activation"] = act
		}
		if act["effectiveStatus"] == nil {
			act["effectiveStatus"] = "enabled"
		}
	}

	metaID := 1000
	nextMeta := func() json.Number {
		metaID++
		return json.Number(strconv.Itoa(metaID))
	}
	var values []map[string]any
	walkMaps(b, func(m map[string]any) {
		if _, ok := m["@id"]; ok {
			values = append(values, m)
		}
	})
	for _, m := range values {
		m["@ns"] = typesNS
		m["@metadata"] = map[string]any{"@id": nextMeta(), "storage": map[string]any{"createTimestamp": stampTime}}
	}

	if len(archetypes) > 0 {
		var archetypeRefs, memberships []any
		for _, oid := range archetypes {
			archetypeRefs = append(archetypeRefs, archetypeRef(oid))
			m := archetypeRef(oid)
			m["@metadata"] = map[string]any{"@ns": typesNS, "@id": nextMeta(), "storage": map[string]any{"createTimestamp": stampTime}}
			memberships = append(memberships, m)
		}
		b["archetypeRef"] = singleOrList(archetypeRefs)
		b["roleMembershipRef"] = singleOrList(memberships)
	}

	if slices.Contains(focusTypes, typ) {
		act, ok := b["activation"].(map[string]any)
		if !ok {
			act = map[string]any{}
			b["activation"] = act
		}
		if act["effectiveStatus"] == nil {
			act["effectiveStatus"] = "enabled"
			act["enableTimestamp"] = stampTime
		}
	}

	b["@metadata"] = map[string]any{
		"@ns":     typesNS,
		"@id":     nextMeta(),
		"storage": map[string]any{"createTimestamp": stampTime, "createChannel": "http://midpoint.evolveum.com/xml/ns/public/common/channels-3#rest"},
		"process": map[string]any{"requestTimestamp": stampTime},
	}
	b["version"] = strconv.Itoa(e.version)
	b["iteration"] = json.Number("0")
	b["iterationToken"] = ""
	return b
}

// annotateRefs gives every reference a c:-prefixed type and the default
// relation, and every targetRef to a stored object its targetName.
func (s *Server) annotateRefs(b map[string]any) {
	walkMaps(b, func(m map[string]any) {
		for k, v := range m {
			if !strings.HasSuffix(k, "Ref") {
				continue
			}
			for _, r := range toList(v) {
				ref, ok := r.(map[string]any)
				if !ok {
					continue
				}
				if t, ok := ref["type"].(string); ok && !strings.Contains(t, ":") {
					ref["type"] = "c:" + t
				}
				if ref["relation"] == nil {
					ref["relation"] = "org:default"
				}
				if oid, ok := ref["oid"].(string); ok && k == "targetRef" {
					if name := s.nameOf(oid); name != "" {
						ref["targetName"] = name
					}
				}
			}
		}
	})
}

// nameOf returns the name of the stored object with oid, of any type.
func (s *Server) nameOf(oid string) string {
	for k, e := range s.objects {
		if k.oid == oid {
			return nameString(e.body["name"])
		}
	}
	return ""
}

func nameString(v any) string {
	switch n := v.(type) {
	case string:
		return n
	case map[string]any:
		s, _ := n["orig"].(string)
		return s
	}
	return ""
}

func archetypeRef(oid string) map[string]any {
	return map[string]any{"oid": oid, "relation": "org:default", "type": "c:ArchetypeType"}
}

func singleOrList(l []any) any {
	if len(l) == 1 {
		return l[0]
	}
	return l
}

func hasRef(v any, oid string) bool {
	for _, r := range toList(v) {
		if ref, ok := r.(map[string]any); ok && ref["oid"] == oid {
			return true
		}
	}
	return false
}

func hasAssignmentTo(v any, oid string) bool {
	for _, a := range toList(v) {
		if am, ok := a.(map[string]any); ok && hasRef(am["targetRef"], oid) {
			return true
		}
	}
	return false
}

// typeName is the midPoint type name of a REST type, e.g. "roles" -> "RoleType".
func typeName(restType string) string {
	s := client.SingularOf(restType)
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:] + "Type"
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError answers with an OperationResult-shaped body.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"status": "fatal_error", "message": msg})
}

func writeNotFound(w http.ResponseWriter, typ, oid string) {
	writeError(w, http.StatusNotFound, fmt.Sprintf("Object of type %s with OID %s not found", typeName(typ), oid))
}

func decode(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	return dec.Decode(v)
}

// clone deep-copies v through JSON, so Go literals become the decoded shape
// (numbers as json.Number). It panics on values JSON cannot encode, which
// only a broken test passes in.
func clone(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mpstub: %v", err))
	}
	var out any
	if err := decode(b, &out); err != nil {
		panic(fmt.Sprintf("mpstub: %v", err))
	}
	return out
}

// walkMaps calls fn for every map in v, outermost first.
func walkMaps(v any, fn func(map[string]any)) {
	switch t := v.(type) {
	case map[string]any:
		fn(t)
		for _, e := range t {
			walkMaps(e, fn)
		}
	case []any:
		for _, e := range t {
			walkMaps(e, fn)
		}
	}
}

// toList returns the values of an item: nil for none, the list itself, or
// a one-element list.
func toList(v any) []any {
	switch t := v.(type) {
	case nil:
		return nil
	case []any:
		return t
	default:
		return []any{t}
	}
}

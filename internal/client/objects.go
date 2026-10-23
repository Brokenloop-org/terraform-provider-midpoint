package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// Object is one midPoint object.
type Object struct {
	// Type is the REST collection type, e.g. "roles". It is empty for
	// search results.
	Type string
	OID  string
	// Body is the object content without the type wrapper.
	Body map[string]any
}

// GetOptions are the read parameters of Get.
type GetOptions struct {
	Options []string
	Exclude []string
}

// ItemDelta is one item modification of an ObjectModificationType.
type ItemDelta struct {
	ModificationType string `json:"modificationType"`
	Path             string `json:"path"`
	Value            any    `json:"value,omitempty"`
}

// Get reads the object typ/oid.
func (c *Client) Get(ctx context.Context, typ, oid string, o GetOptions) (*Object, error) {
	q := withOptions(o.Options)
	for _, e := range o.Exclude {
		q.Add("exclude", e)
	}
	_, body, err := c.do(ctx, http.MethodGet, objectPath(typ, oid), q, nil)
	if err != nil {
		return nil, err
	}

	var doc map[string]any
	if err := decode(body, &doc); err != nil {
		return nil, fmt.Errorf("decoding %s/%s: %w", typ, oid, err)
	}
	content, err := unwrap(doc, SingularOf(typ))
	if err != nil {
		return nil, fmt.Errorf("decoding %s/%s: %w", typ, oid, err)
	}
	obj := &Object{Type: typ, OID: oid, Body: content}
	if s, ok := content["oid"].(string); ok && s != "" {
		obj.OID = s
	}
	return obj, nil
}

// Create adds obj and returns the OID midPoint assigned or kept.
func (c *Client) Create(ctx context.Context, obj Object, options []string) (string, error) {
	resp, _, err := c.do(ctx, http.MethodPost, "/"+url.PathEscape(obj.Type), withOptions(options), Wrap(obj))
	if err != nil {
		return "", err
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("creating %s: midPoint returned HTTP %d without a Location header", obj.Type, resp.StatusCode)
	}
	u, err := url.Parse(loc)
	if err != nil {
		return "", fmt.Errorf("creating %s: invalid Location header %q: %w", obj.Type, loc, err)
	}
	oid := path.Base(strings.TrimRight(u.Path, "/"))
	if oid == "" || oid == "." || oid == "/" {
		return "", fmt.Errorf("creating %s: no OID in Location header %q", obj.Type, loc)
	}
	return oid, nil
}

// Modify applies deltas to typ/oid.
func (c *Client) Modify(ctx context.Context, typ, oid string, deltas []ItemDelta, options []string) error {
	body := map[string]any{"objectModification": map[string]any{"itemDelta": deltas}}
	_, _, err := c.do(ctx, http.MethodPatch, objectPath(typ, oid), withOptions(options), body)
	return err
}

// Delete removes typ/oid. A missing object yields an error matching ErrNotFound.
func (c *Client) Delete(ctx context.Context, typ, oid string, options []string) error {
	_, _, err := c.do(ctx, http.MethodDelete, objectPath(typ, oid), withOptions(options), nil)
	return err
}

// Search runs an Axiom query filter against typ. An empty filter matches all
// objects and maxSize <= 0 means no paging limit.
//
// total is midPoint's total match count from the X-Total-Count header. -1
// means the server did not report a total; callers treat len(objs) as a lower
// bound. midPoint 4.9 never sends the header, although Search still asks for
// it with returnTotalCount=true.
//
// midPoint returns the matches as an ObjectListType under "object":
// {"@ns": ..., "object": {"@type": "...#ObjectListType", "object": [...]}}.
// The inner "object" key is absent when nothing matches.
func (c *Client) Search(ctx context.Context, typ, axiomFilter string, maxSize int, options []string) (objs []Object, total int, err error) {
	query := map[string]any{}
	if axiomFilter != "" {
		query["filter"] = map[string]any{"text": axiomFilter}
	}
	if maxSize > 0 {
		query["paging"] = map[string]any{"maxSize": maxSize}
	}
	q := withOptions(options)
	q.Set("returnTotalCount", "true")

	resp, body, err := c.do(ctx, http.MethodPost, "/"+url.PathEscape(typ)+"/search", q, map[string]any{"query": query})
	if err != nil {
		return nil, 0, err
	}
	var doc map[string]any
	if err := decode(body, &doc); err != nil {
		return nil, 0, fmt.Errorf("decoding %s search result: %w", typ, err)
	}

	listType, ok := doc["object"].(map[string]any)
	if !ok {
		return nil, 0, fmt.Errorf("decoding %s search result: want an ObjectListType object under \"object\", got %s", typ, describe(doc["object"]))
	}
	var list []any
	if inner, present := listType["object"]; present {
		if list, ok = inner.([]any); !ok {
			return nil, 0, fmt.Errorf("decoding %s search result: want a list under \"object\".\"object\", got %s", typ, describe(inner))
		}
	}

	objs = make([]Object, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, 0, fmt.Errorf("decoding %s search result: want objects in the result list, got %s", typ, describe(item))
		}
		oid, _ := m["oid"].(string)
		objs = append(objs, Object{OID: oid, Body: m})
	}

	h := resp.Header.Get("X-Total-Count")
	if h == "" {
		return objs, -1, nil
	}
	total, err = strconv.Atoi(h)
	if err != nil {
		return nil, 0, fmt.Errorf("decoding %s search result: invalid X-Total-Count %q", typ, h)
	}
	return objs, total, nil
}

// describe names the JSON type of v for error messages.
func describe(v any) string {
	switch v.(type) {
	case nil:
		return "nothing"
	case map[string]any:
		return "an object"
	case []any:
		return "a list"
	case string:
		return "a string"
	case json.Number:
		return "a number"
	case bool:
		return "a boolean"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// ServerVersion returns the build version of the node serving the request.
func (c *Client) ServerVersion(ctx context.Context) (string, error) {
	node, err := c.Get(ctx, "nodes", "current", GetOptions{})
	if err != nil {
		return "", err
	}
	build, _ := node.Body["build"].(map[string]any)
	version, _ := build["version"].(string)
	if version == "" {
		return "", errors.New("midPoint node has no build version")
	}
	return version, nil
}

// Wrap returns obj's body under its singular type key, e.g. {"role": body}.
func Wrap(obj Object) map[string]any {
	return map[string]any{SingularOf(obj.Type): obj.Body}
}

// SingularOf returns the JSON element name for a REST collection type:
// "roles" -> "role", "functionLibraries" -> "functionLibrary",
// "objects" -> "object", "focus" -> "focus".
func SingularOf(restType string) string {
	switch {
	case restType == "focus":
		return "focus"
	case restType == "objects":
		return "object"
	case strings.HasSuffix(restType, "ies"):
		return strings.TrimSuffix(restType, "ies") + "y"
	default:
		return strings.TrimSuffix(restType, "s")
	}
}

// objectPath is the path of one object below the REST base path.
func objectPath(typ, oid string) string {
	return "/" + url.PathEscape(typ) + "/" + url.PathEscape(oid)
}

// unwrap returns the object under the wrapper key. It prefers the expected
// element name and otherwise accepts the only key that is not a JSON-LD
// style "@" annotation (generic collections answer with the concrete type).
func unwrap(doc map[string]any, singular string) (map[string]any, error) {
	if v, ok := doc[singular].(map[string]any); ok {
		return v, nil
	}
	var found map[string]any
	for k, v := range doc {
		if strings.HasPrefix(k, "@") {
			continue
		}
		m, ok := v.(map[string]any)
		if !ok || found != nil {
			return nil, fmt.Errorf("expected a single %q object in the response", singular)
		}
		found = m
	}
	if found == nil {
		return nil, fmt.Errorf("expected a single %q object in the response", singular)
	}
	return found, nil
}

// BuiltinOIDPrefix is the oid range of the objects midPoint ships
// (00000000-0000-0000-0000-*).
const BuiltinOIDPrefix = "00000000-0000-0000-0000-"

// IsBuiltinOID reports whether oid is in the range of the objects midPoint
// ships, which exist before Terraform creates them.
func IsBuiltinOID(oid string) bool { return strings.HasPrefix(oid, BuiltinOIDPrefix) }

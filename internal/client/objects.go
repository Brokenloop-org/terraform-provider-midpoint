package client

import (
	"context"
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
// objects and maxSize <= 0 means no paging limit. total is midPoint's total
// match count, or len(objs) when the server does not report it.
func (c *Client) Search(ctx context.Context, typ, axiomFilter string, maxSize int, options []string) ([]Object, int, error) {
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

	items := doc["object"]
	// The result may be an ObjectListType wrapping the list once more.
	if m, ok := items.(map[string]any); ok {
		if inner, ok := m["object"]; ok {
			items = inner
		}
	}
	var list []any
	switch v := items.(type) {
	case nil:
	case []any:
		list = v
	case map[string]any:
		// midPoint renders a single-valued occurrence as an object.
		list = []any{v}
	default:
		return nil, 0, fmt.Errorf("decoding %s search result: unexpected %T under \"object\"", typ, items)
	}

	objs := make([]Object, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, 0, fmt.Errorf("decoding %s search result: unexpected %T in result list", typ, item)
		}
		oid, _ := m["oid"].(string)
		objs = append(objs, Object{OID: oid, Body: m})
	}

	total := len(objs)
	if h := resp.Header.Get("X-Total-Count"); h != "" {
		n, err := strconv.Atoi(h)
		if err != nil {
			return nil, 0, fmt.Errorf("decoding %s search result: invalid X-Total-Count %q", typ, h)
		}
		total = n
	}
	return objs, total, nil
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

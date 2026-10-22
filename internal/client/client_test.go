package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// recorded is what the stub server saw for one request.
type recorded struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   []byte
}

// reply is what the stub server answers.
type reply struct {
	Status int
	Header map[string]string
	Body   string
}

// stub starts a server that records every request and answers with r.
func stub(t *testing.T, r reply) (*Client, *[]recorded) {
	t.Helper()
	var seen []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Errorf("reading request body: %v", err)
		}
		seen = append(seen, recorded{
			Method: req.Method,
			Path:   req.URL.Path,
			Query:  req.URL.Query(),
			Header: req.Header.Clone(),
			Body:   body,
		})
		for k, v := range r.Header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(r.Status)
		_, _ = io.WriteString(w, r.Body)
	}))
	t.Cleanup(srv.Close)
	c, err := New(Config{URL: srv.URL, Username: "administrator", Password: "5ecr3t"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c, &seen
}

// only returns the single recorded request.
func only(t *testing.T, seen *[]recorded) recorded {
	t.Helper()
	if len(*seen) != 1 {
		t.Fatalf("server saw %d requests, want 1", len(*seen))
	}
	return (*seen)[0]
}

// assertJSON compares a request body with the expected JSON document.
func assertJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("request body %q is not JSON: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expected JSON: %v", err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("request body = %s, want %s", got, want)
	}
}

func TestGet_unwrapsAndSendsOptions(t *testing.T) {
	c, seen := stub(t, reply{
		Status: 200,
		Body:   `{"@ns":"http://prism.evolveum.com/xml/ns/public/types-3","role":{"oid":"1","name":"x","riskLevel":3}}`,
	})

	obj, err := c.Get(context.Background(), "roles", "1", GetOptions{Options: []string{"noFetch"}, Exclude: []string{"metadata"}})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	req := only(t, seen)
	if req.Method != http.MethodGet || req.Path != "/midpoint/ws/rest/roles/1" {
		t.Errorf("request = %s %s, want GET /midpoint/ws/rest/roles/1", req.Method, req.Path)
	}
	wantQuery := url.Values{"options": {"noFetch"}, "exclude": {"metadata"}}
	if !reflect.DeepEqual(req.Query, wantQuery) {
		t.Errorf("query = %v, want %v", req.Query, wantQuery)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("administrator:5ecr3t"))
	if got := req.Header.Get("Authorization"); got != wantAuth {
		t.Errorf("Authorization = %q, want %q", got, wantAuth)
	}
	if got := req.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want application/json", got)
	}

	if obj.Type != "roles" || obj.OID != "1" {
		t.Errorf("Type/OID = %q/%q, want roles/1", obj.Type, obj.OID)
	}
	if obj.Body["name"] != "x" {
		t.Errorf("Body[name] = %v, want x", obj.Body["name"])
	}
	if obj.Body["riskLevel"] != json.Number("3") {
		t.Errorf("Body[riskLevel] = %#v, want json.Number(\"3\")", obj.Body["riskLevel"])
	}
}

func TestGet_genericCollectionUnwrapsConcreteType(t *testing.T) {
	c, seen := stub(t, reply{Status: 200, Body: `{"@ns":"x","service":{"oid":"s1","name":"svc"}}`})

	obj, err := c.Get(context.Background(), "objects", "s1", GetOptions{})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if req := only(t, seen); req.Path != "/midpoint/ws/rest/objects/s1" {
		t.Errorf("path = %q, want /midpoint/ws/rest/objects/s1", req.Path)
	}
	if obj.Type != "objects" || obj.OID != "s1" || obj.Body["name"] != "svc" {
		t.Errorf("obj = %+v", obj)
	}
}

func TestGet_notFound(t *testing.T) {
	c, _ := stub(t, reply{Status: 404, Body: `{"operationResult":{"status":"fatal_error","message":"Object of type 'RoleType' with OID '1' was not found."}}`})

	_, err := c.Get(context.Background(), "roles", "1", GetOptions{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 404 {
		t.Fatalf("err = %#v, want *APIError with status 404", err)
	}
	if !strings.Contains(err.Error(), "was not found") {
		t.Errorf("Error() = %q, want midPoint message", err.Error())
	}
}

func TestCreate_readsLocationHeader(t *testing.T) {
	tests := []struct {
		name     string
		obj      Object
		status   int
		location string
		wantPath string
		wantBody string
		wantOID  string
	}{
		{
			name:     "role",
			obj:      Object{Type: "roles", Body: map[string]any{"name": "r", "description": "d"}},
			status:   201,
			location: "http://x/midpoint/ws/rest/roles/abc",
			wantPath: "/midpoint/ws/rest/roles",
			wantBody: `{"role":{"name":"r","description":"d"}}`,
			wantOID:  "abc",
		},
		{
			name:     "task answers 202",
			obj:      Object{Type: "tasks", Body: map[string]any{"oid": "t1", "name": "t"}},
			status:   202,
			location: "http://x/midpoint/ws/rest/tasks/t1",
			wantPath: "/midpoint/ws/rest/tasks",
			wantBody: `{"task":{"oid":"t1","name":"t"}}`,
			wantOID:  "t1",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, seen := stub(t, reply{Status: tc.status, Header: map[string]string{"Location": tc.location}})

			oid, err := c.Create(context.Background(), tc.obj, []string{"isImport", "raw"})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}
			if oid != tc.wantOID {
				t.Errorf("oid = %q, want %q", oid, tc.wantOID)
			}
			req := only(t, seen)
			if req.Method != http.MethodPost || req.Path != tc.wantPath {
				t.Errorf("request = %s %s, want POST %s", req.Method, req.Path, tc.wantPath)
			}
			if got := req.Query["options"]; !reflect.DeepEqual(got, []string{"isImport", "raw"}) {
				t.Errorf("options = %v, want repeated [isImport raw]", got)
			}
			if got := req.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got)
			}
			assertJSON(t, req.Body, tc.wantBody)
		})
	}
}

func TestCreate_missingLocation(t *testing.T) {
	c, _ := stub(t, reply{Status: 201})
	if _, err := c.Create(context.Background(), Object{Type: "roles", Body: map[string]any{"name": "r"}}, nil); err == nil {
		t.Fatal("Create without Location header must fail")
	}
}

func TestCreate_conflictIsAPIError(t *testing.T) {
	body := `{"@ns":"http://prism.evolveum.com/xml/ns/public/types-3","status":"fatal_error","message":"Object already exists"}`
	c, _ := stub(t, reply{Status: 409, Body: body})

	_, err := c.Create(context.Background(), Object{Type: "roles", Body: map[string]any{"name": "r"}}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %#v, want *APIError", err)
	}
	if apiErr.Status != 409 || apiErr.Message != "Object already exists" || apiErr.Body != body {
		t.Errorf("APIError = %+v", apiErr)
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("409 must not be ErrNotFound")
	}
	if msg := err.Error(); !strings.Contains(msg, "409") || !strings.Contains(msg, "Object already exists") {
		t.Errorf("Error() = %q, want status and message", msg)
	}
}

func TestAPIError_message(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantMessage string
		wantInError string
	}{
		{"top-level message", `{"message":"top"}`, "top", "top"},
		{"nested under operationResult", `{"operationResult":{"message":"nested"}}`, "nested", "nested"},
		{"not JSON", `<html>gateway</html>`, "", "<html>gateway</html>"},
		{"empty body", ``, "", "500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := stub(t, reply{Status: 500, Body: tc.body})
			err := c.Delete(context.Background(), "roles", "1", nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %#v, want *APIError", err)
			}
			if apiErr.Message != tc.wantMessage {
				t.Errorf("Message = %q, want %q", apiErr.Message, tc.wantMessage)
			}
			if !strings.Contains(err.Error(), tc.wantInError) {
				t.Errorf("Error() = %q, want it to contain %q", err.Error(), tc.wantInError)
			}
		})
	}
}

func TestModify_sendsObjectModification(t *testing.T) {
	c, seen := stub(t, reply{Status: 204})

	deltas := []ItemDelta{{ModificationType: "replace", Path: "description", Value: "d"}}
	if err := c.Modify(context.Background(), "roles", "1", deltas, []string{"raw"}); err != nil {
		t.Fatalf("Modify: %v", err)
	}

	req := only(t, seen)
	if req.Method != http.MethodPatch || req.Path != "/midpoint/ws/rest/roles/1" {
		t.Errorf("request = %s %s, want PATCH /midpoint/ws/rest/roles/1", req.Method, req.Path)
	}
	if got := req.Query["options"]; !reflect.DeepEqual(got, []string{"raw"}) {
		t.Errorf("options = %v, want [raw]", got)
	}
	assertJSON(t, req.Body, `{"objectModification":{"itemDelta":[{"modificationType":"replace","path":"description","value":"d"}]}}`)
}

func TestDelete(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		options []string
		wantErr error
	}{
		{name: "deleted", status: 204},
		{name: "with options", status: 204, options: []string{"force", "raw"}},
		{name: "not found", status: 404, wantErr: ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, seen := stub(t, reply{Status: tc.status})
			err := c.Delete(context.Background(), "roles", "1", tc.options)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			req := only(t, seen)
			if req.Method != http.MethodDelete || req.Path != "/midpoint/ws/rest/roles/1" {
				t.Errorf("request = %s %s, want DELETE /midpoint/ws/rest/roles/1", req.Method, req.Path)
			}
			if got := req.Query["options"]; !reflect.DeepEqual(got, tc.options) {
				t.Errorf("options = %v, want %v", got, tc.options)
			}
		})
	}
}

func TestSearch_textFilterAndTotalCount(t *testing.T) {
	c, seen := stub(t, reply{
		Status: 200,
		Header: map[string]string{"X-Total-Count": "12"},
		Body:   `{"object":[{"oid":"u1","name":"a","@type":"c:UserType"}]}`,
	})

	objs, total, err := c.Search(context.Background(), "users", `roleMembershipRef matches (oid = "1")`, 5, nil)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	req := only(t, seen)
	if req.Method != http.MethodPost || req.Path != "/midpoint/ws/rest/users/search" {
		t.Errorf("request = %s %s, want POST /midpoint/ws/rest/users/search", req.Method, req.Path)
	}
	if got := req.Query.Get("returnTotalCount"); got != "true" {
		t.Errorf("returnTotalCount = %q, want true", got)
	}
	assertJSON(t, req.Body, `{"query":{"filter":{"text":"roleMembershipRef matches (oid = \"1\")"},"paging":{"maxSize":5}}}`)

	if total != 12 {
		t.Errorf("total = %d, want 12", total)
	}
	want := []Object{{OID: "u1", Body: map[string]any{"oid": "u1", "name": "a", "@type": "c:UserType"}}}
	if !reflect.DeepEqual(objs, want) {
		t.Errorf("objs = %#v, want %#v", objs, want)
	}
}

func TestSearch_resultShapes(t *testing.T) {
	tests := []struct {
		name      string
		header    map[string]string
		body      string
		wantOIDs  []string
		wantTotal int
		wantErr   string
	}{
		{
			name:      "single result as object",
			header:    map[string]string{"X-Total-Count": "1"},
			body:      `{"object":{"oid":"u1","name":"a"}}`,
			wantOIDs:  []string{"u1"},
			wantTotal: 1,
		},
		{
			name:      "ObjectListType wrapper",
			header:    map[string]string{"X-Total-Count": "2"},
			body:      `{"@ns":"x","object":{"@type":"ObjectListType","object":[{"oid":"u1"},{"oid":"u2"}]}}`,
			wantOIDs:  []string{"u1", "u2"},
			wantTotal: 2,
		},
		{
			name:      "no results",
			header:    map[string]string{"X-Total-Count": "0"},
			body:      `{}`,
			wantTotal: 0,
		},
		{
			name:    "missing total header",
			body:    `{"object":[{"oid":"u1"},{"oid":"u2"}]}`,
			wantErr: "X-Total-Count",
		},
		{
			name:    "invalid total header",
			header:  map[string]string{"X-Total-Count": "many"},
			body:    `{"object":[{"oid":"u1"}]}`,
			wantErr: "X-Total-Count",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := stub(t, reply{Status: 200, Header: tc.header, Body: tc.body})
			objs, total, err := c.Search(context.Background(), "users", "", 0, nil)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want error mentioning %s", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Search: %v", err)
			}
			var oids []string
			for _, o := range objs {
				oids = append(oids, o.OID)
			}
			if !reflect.DeepEqual(oids, tc.wantOIDs) {
				t.Errorf("oids = %v, want %v", oids, tc.wantOIDs)
			}
			if total != tc.wantTotal {
				t.Errorf("total = %d, want %d", total, tc.wantTotal)
			}
		})
	}
}

func TestSearch_omitsEmptyFilterAndPaging(t *testing.T) {
	c, seen := stub(t, reply{Status: 200, Header: map[string]string{"X-Total-Count": "0"}, Body: `{}`})
	if _, _, err := c.Search(context.Background(), "roles", "", 0, []string{"raw"}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	req := only(t, seen)
	assertJSON(t, req.Body, `{"query":{}}`)
	if got := req.Query["options"]; !reflect.DeepEqual(got, []string{"raw"}) {
		t.Errorf("options = %v, want [raw]", got)
	}
}

func TestServerVersion(t *testing.T) {
	c, seen := stub(t, reply{Status: 200, Body: `{"node":{"oid":"n1","build":{"version":"4.9.9"}}}`})

	v, err := c.ServerVersion(context.Background())
	if err != nil {
		t.Fatalf("ServerVersion: %v", err)
	}
	if v != "4.9.9" {
		t.Errorf("version = %q, want 4.9.9", v)
	}
	req := only(t, seen)
	if req.Method != http.MethodGet || req.Path != "/midpoint/ws/rest/nodes/current" {
		t.Errorf("request = %s %s, want GET /midpoint/ws/rest/nodes/current", req.Method, req.Path)
	}
}

func TestServerVersion_missingVersion(t *testing.T) {
	c, _ := stub(t, reply{Status: 200, Body: `{"node":{"oid":"n1"}}`})
	if _, err := c.ServerVersion(context.Background()); err == nil {
		t.Fatal("ServerVersion without build.version must fail")
	}
}

func TestSingularOf(t *testing.T) {
	tests := map[string]string{
		"roles":             "role",
		"objectTemplates":   "objectTemplate",
		"focus":             "focus",
		"objects":           "object",
		"functionLibraries": "functionLibrary",
		"securityPolicies":  "securityPolicy",
		"nodes":             "node",
		"assignmentHolders": "assignmentHolder",
	}
	for in, want := range tests {
		if got := SingularOf(in); got != want {
			t.Errorf("SingularOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWrap(t *testing.T) {
	body := map[string]any{"name": "lib"}
	got := Wrap(Object{Type: "functionLibraries", Body: body})
	want := map[string]any{"functionLibrary": body}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Wrap = %v, want %v", got, want)
	}
}

func TestNew_invalidConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{"empty URL", Config{}},
		{"URL without scheme", Config{URL: "midpoint.example.com"}},
		{"unsupported scheme", Config{URL: "ftp://midpoint.example.com"}},
		{"CA bundle without certificates", Config{URL: "https://midpoint.example.com", CABundle: []byte("not a pem")}},
		{"URL with credentials", Config{URL: "https://admin:s3cret@midpoint.example.com"}},
		{"unparsable URL with credentials", Config{URL: "https://admin:s3cret@midpoint.example.com:port"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.cfg)
			if err == nil {
				t.Fatal("New must fail")
			}
			if strings.Contains(err.Error(), "s3cret") {
				t.Errorf("error %q leaks the URL password", err.Error())
			}
		})
	}
}

func TestNew_userAgentAndTrailingSlash(t *testing.T) {
	var gotPath, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotUA = r.URL.Path, r.Header.Get("User-Agent")
		w.WriteHeader(204)
	}))
	t.Cleanup(srv.Close)

	c, err := New(Config{URL: srv.URL + "/", UserAgent: "terraform-provider-midpoint/test"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Delete(context.Background(), "roles", "1", nil); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if gotPath != "/midpoint/ws/rest/roles/1" {
		t.Errorf("path = %q", gotPath)
	}
	if gotUA != "terraform-provider-midpoint/test" {
		t.Errorf("User-Agent = %q", gotUA)
	}
}

func TestNew_TLS(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"node":{"build":{"version":"4.9.9"}}}`)
	}))
	// The untrusted case fails the handshake on purpose; keep test output clean.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})

	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{name: "untrusted certificate", cfg: Config{URL: srv.URL}, wantErr: true},
		{name: "CA bundle", cfg: Config{URL: srv.URL, CABundle: caPEM}},
		{name: "insecure", cfg: Config{URL: srv.URL, Insecure: true}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(tc.cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			_, err = c.ServerVersion(context.Background())
			if (err != nil) != tc.wantErr {
				t.Errorf("ServerVersion err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

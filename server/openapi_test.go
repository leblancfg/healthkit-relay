package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// Routes the OpenAPI document leaves out on purpose: the app's upload and pairing API
// (server/README.md), the setup page, health and metrics, and the docs page itself.
var undocumented = map[string]bool{
	"GET /healthz": true, "GET /metrics": true, "POST /v1/samples": true, "POST /v1/heartbeat": true, "GET /v1/status": true,
	"DELETE /v1/data": true, "POST /v1/pair": true, "POST /v1/pairing-codes": true,
	"GET /setup": true, "POST /setup": true, "POST /setup/revoke": true, "POST /setup/tokens": true,
	"GET /setup/status": true, "GET /{$}": true, "GET /docs": true, "GET /docs/{file}": true,
}

type specOp struct {
	OperationID string `json:"operationId"`
	Scope       string `json:"x-required-scope"`
	Security    *[]map[string][]string
	Parameters  []struct {
		Ref     string `json:"$ref"`
		Name    string
		In      string
		Example any
	}
	Responses map[string]json.RawMessage
}

func loadSpec(t *testing.T, s *Server) (map[string]map[string]specOp, map[string]any) {
	t.Helper()
	r := httptest.NewRequest("GET", "/openapi.json", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("/openapi.json: %d", w.Code)
	}
	var doc struct {
		OpenAPI string `json:"openapi"`
		Paths   map[string]map[string]specOp
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("openapi %q", doc.OpenAPI)
	}
	var raw map[string]any
	json.Unmarshal(w.Body.Bytes(), &raw)
	return doc.Paths, raw
}

// routeKey is a route pattern for an OpenAPI method and path: get /v1/types -> "GET /v1/types".
func routeKey(method, path string) string { return strings.ToUpper(method) + " " + path }

// Every operation in the document is served, every served route is in the document or
// listed as undocumented, and each operation's scope is what the handler enforces.
func TestOpenAPIMatchesRoutes(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "duckdb")
	os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755) // queries print nothing: every type is empty
	db, err := NewDuckDB(fake, dir, 5*time.Second, 100, "64MB", 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	s := testServer(t)
	s.Query = &queryAPI{db: db, now: time.Now}
	read, _, _ := s.Devices.AddScoped("grafana", "", ScopeRead)
	upload, _, _ := s.Devices.Add("iPhone", "")
	_, victim, _ := s.Devices.AddScoped("to-revoke", "", ScopeRead)
	paths, _ := loadSpec(t, s)

	served := map[string]bool{}
	for _, rt := range s.routes() {
		served[rt.pattern] = true
	}
	inSpec := map[string]bool{}
	for path, ops := range paths {
		for method, op := range ops {
			key := routeKey(method, path)
			inSpec[key] = true
			if !served[key] {
				t.Errorf("%s is in the document but not served", key)
			}
			if op.OperationID == "" || op.Responses["401"] == nil && op.Scope != "none" {
				t.Errorf("%s: needs an operationId and a 401 response", key)
			}
			// Call it: no token, an upload token, a read token, the server token.
			target := path
			for _, p := range op.Parameters {
				if p.Ref == "#/components/parameters/Type" {
					target = strings.ReplaceAll(target, "{type}", stepsType)
				} else if p.In == "path" {
					target = strings.ReplaceAll(target, "{"+p.Name+"}", victim.ID)
				}
			}
			do := func(token string) int {
				var body *bytes.Reader
				if method == "post" {
					body = bytes.NewReader([]byte(`{"name":"spec-test"}`))
				} else {
					body = bytes.NewReader(nil)
				}
				r := httptest.NewRequest(strings.ToUpper(method), target, body)
				if token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				w := httptest.NewRecorder()
				s.Handler().ServeHTTP(w, r)
				return w.Code
			}
			none, up, rd, admin := do(""), do(upload), do(read), do(testToken)
			switch op.Scope {
			case "read":
				if none != 401 || up != 403 || rd != 200 || admin != 200 {
					t.Errorf("%s (read): none %d, upload %d, read %d, admin %d", key, none, up, rd, admin)
				}
				if op.Responses["403"] == nil {
					t.Errorf("%s: no 403 response", key)
				}
			case "admin":
				if none != 401 || up != 401 || rd != 401 || admin >= 300 {
					t.Errorf("%s (admin): none %d, upload %d, read %d, admin %d", key, none, up, rd, admin)
				}
			case "none":
				if none != 200 || op.Security == nil || len(*op.Security) != 0 {
					t.Errorf("%s (public): %d, security %v", key, none, op.Security)
				}
			default:
				t.Errorf("%s: x-required-scope %q", key, op.Scope)
			}
		}
	}
	var missing []string
	for key := range served {
		if !inSpec[key] && !undocumented[key] {
			missing = append(missing, key)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("served but not in the document (or the undocumented list): %v", missing)
	}
	for key := range undocumented {
		if !served[key] {
			t.Errorf("undocumented list names %s, which is not served", key)
		}
	}
}

// The document is complete JSON with every $ref resolving, kinds come from Kinds, and
// types are described by a pattern.
func TestOpenAPIDocument(t *testing.T) {
	s := testServer(t)
	s.Query = &queryAPI{db: &DuckDB{}, now: time.Now}
	_, raw := loadSpec(t, s)
	var refs []string
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, x := range v {
				if k == "$ref" {
					refs = append(refs, x.(string))
				}
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(raw)
	for _, ref := range refs {
		var cur any = raw
		for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
			m, _ := cur.(map[string]any)
			cur = m[part]
		}
		if cur == nil {
			t.Errorf("$ref %s does not resolve", ref)
		}
	}
	schemas := raw["components"].(map[string]any)["schemas"].(map[string]any)
	// Types: a pattern every catalog identifier matches, not an enum of 200 names.
	ht := schemas["HealthType"].(map[string]any)
	if ht["enum"] != nil {
		t.Error("HealthType has an enum")
	}
	re := regexp.MustCompile(ht["pattern"].(string))
	for _, ct := range catalogTypes {
		if !re.MatchString(ct.Identifier) {
			t.Errorf("%s does not match the HealthType pattern %s", ct.Identifier, re)
		}
	}
	kind := schemas["Kind"].(map[string]any)
	if kinds := kind["enum"].([]any); len(kinds) != len(Kinds) {
		t.Errorf("Kind enum: %v", kinds)
	}
	for _, k := range Kinds {
		if line := "\n- `" + k.Name + "`: " + k.Description; !strings.Contains(kind["description"].(string), line) || strings.Contains(k.Description, "\n") {
			t.Errorf("Kind description lacks the line %q", line)
		}
	}
	if v := raw["info"].(map[string]any)["version"]; v != version {
		t.Errorf("info.version %v", v)
	}
}

// Every parameter and every JSON success response has an example.
func TestOpenAPIExamples(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal(openAPITemplate, &doc); err != nil {
		t.Fatal(err)
	}
	has := func(m map[string]any) bool { return m["example"] != nil || m["examples"] != nil }
	params := doc["components"].(map[string]any)["parameters"].(map[string]any)
	for path, ops := range doc["paths"].(map[string]any) {
		for method, op := range ops.(map[string]any) {
			key := routeKey(method, path)
			op := op.(map[string]any)
			ps, _ := op["parameters"].([]any)
			for _, p := range ps {
				p := p.(map[string]any)
				if ref, ok := p["$ref"].(string); ok {
					p = params[strings.TrimPrefix(ref, "#/components/parameters/")].(map[string]any)
				}
				if !has(p) {
					t.Errorf("%s: parameter %v has no example", key, p["name"])
				}
			}
			for code, resp := range op["responses"].(map[string]any) {
				content, _ := resp.(map[string]any)["content"].(map[string]any)
				if body, ok := content["application/json"].(map[string]any); ok && code[0] == '2' && !has(body) {
					t.Errorf("%s: %s response has no example", key, code)
				}
			}
		}
	}
}

// The vendored Swagger UI files are the ones scripts/vendor-swagger-ui.sh checked.
func TestSwaggerUIVendored(t *testing.T) {
	sums, err := os.ReadFile("swaggerui/SHA256SUMS")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(sums)), "\n")
	if !strings.HasPrefix(lines[0], "swagger-ui-dist 5.") {
		t.Errorf("SHA256SUMS header: %s", lines[0])
	}
	for _, l := range lines[1:] {
		want, name, _ := strings.Cut(l, "  ")
		b, err := swaggerUI.ReadFile("swaggerui/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != want {
			t.Errorf("%s: sha256 %s, want %s", name, got, want)
		}
	}
	page, _ := swaggerUI.ReadFile("swaggerui/index.html")
	init, _ := swaggerUI.ReadFile("swaggerui/init.js")
	if bytes.Contains(page, []byte("//")) && bytes.Contains(page, []byte("https://")) || !bytes.Contains(init, []byte("validatorUrl: null")) {
		t.Error("/docs must load nothing from elsewhere")
	}
}

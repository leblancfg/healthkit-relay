package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"net/http"
	"path"
	"sync"
)

// /openapi.json describes the query API (and token management); /docs shows it with Swagger
// UI, vendored in swaggerui/ (scripts/vendor-swagger-ui.sh) so nothing loads from a CDN.
// Neither needs a token: they hold no health data.

//go:embed openapi.json
var openAPITemplate []byte

//go:embed swaggerui
var swaggerUI embed.FS

var openAPIDoc = sync.OnceValues(func() ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(openAPITemplate, &doc); err != nil {
		return nil, err
	}
	doc["info"].(map[string]any)["version"] = version
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	schemas["HealthType"].(map[string]any)["enum"] = catalogIDs()
	schemas["Kind"].(map[string]any)["enum"] = kindNames()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	err := enc.Encode(doc)
	return b.Bytes(), err
})

func (s *Server) openAPI(w http.ResponseWriter, r *http.Request) {
	doc, err := openAPIDoc()
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*") // public, no credentials: lets other tools load it
	w.Write(doc)
}

var docsTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".png":  "image/png",
}

func (s *Server) docs(w http.ResponseWriter, r *http.Request) {
	name := "index.html"
	if f := r.PathValue("file"); f != "" {
		name = f
	}
	ct, ok := docsTypes[path.Ext(name)]
	b, err := swaggerUI.ReadFile("swaggerui/" + name)
	if !ok || err != nil || name == "index.html" && r.PathValue("file") != "" {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	// Swagger UI sets inline styles; scripts come from this server only.
	h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
		"connect-src 'self'; font-src 'self' data:; form-action 'none'; frame-ancestors 'none'; base-uri 'none'")
	if name == "index.html" {
		h.Set("Cache-Control", "no-cache")
	} else {
		h.Set("Cache-Control", "public, max-age=86400")
	}
	w.Write(b)
}

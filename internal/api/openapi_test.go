package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestServeOpenAPISpec(t *testing.T) {
	rec := doGet(NewRouter(&stubService{}), "/openapi.json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	var spec map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("spec is not valid JSON: %v", err)
	}
	paths, ok := spec["paths"].(map[string]any)
	if !ok || len(paths) == 0 {
		t.Fatalf("spec has no paths: %v", spec["paths"])
	}
	if _, ok := paths["/addresses/{address}/tokens"]; !ok {
		t.Errorf("spec missing tokens path; got paths %v", paths)
	}
}

func TestServeDocs(t *testing.T) {
	rec := doGet(NewRouter(&stubService{}), "/docs")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/openapi.json") {
		t.Errorf("docs page does not reference /openapi.json")
	}
	if !strings.Contains(body, "https://cdn.jsdelivr.net/npm/redoc@2.5.3/bundles/redoc.standalone.js") {
		t.Errorf("docs page does not load the pinned redoc bundle")
	}
}

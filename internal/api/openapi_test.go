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
	walletPath, ok := paths["/wallet/{address}"].(map[string]any)
	if !ok {
		t.Fatalf("spec missing wallet path; got paths %v", paths)
	}
	walletGet, ok := walletPath["get"].(map[string]any)
	if !ok {
		t.Fatalf("wallet path has no GET operation: %v", walletPath)
	}
	walletResponses, ok := walletGet["responses"].(map[string]any)
	if !ok {
		t.Fatalf("wallet GET has no responses: %v", walletGet)
	}
	for _, code := range []string{"200", "400", "500", "502", "503"} {
		if _, ok := walletResponses[code]; !ok {
			t.Errorf("wallet GET responses missing %s: %v", code, walletResponses)
		}
	}
	walletOK, ok := walletResponses["200"].(map[string]any)
	if !ok {
		t.Fatalf("wallet GET has no 200 response: %v", walletResponses)
	}
	walletSchema, ok := walletOK["schema"].(map[string]any)
	if !ok || walletSchema["$ref"] != "#/definitions/wallet-api_internal_wallet.TokenPortfolio" {
		t.Errorf("wallet 200 schema = %v, want TokenPortfolio reference", walletOK["schema"])
	}
	nativePath, ok := paths["/native"].(map[string]any)
	if !ok {
		t.Fatalf("spec missing native path; got paths %v", paths)
	}
	get, ok := nativePath["get"].(map[string]any)
	if !ok {
		t.Fatalf("native path has no GET operation: %v", nativePath)
	}
	responses, ok := get["responses"].(map[string]any)
	if !ok {
		t.Fatalf("native GET has no responses: %v", get)
	}
	okResponse, ok := responses["200"].(map[string]any)
	if !ok {
		t.Fatalf("native GET has no 200 response: %v", responses)
	}
	schema, ok := okResponse["schema"].(map[string]any)
	if !ok || schema["type"] != "array" {
		t.Fatalf("native 200 schema = %v, want array", okResponse["schema"])
	}
	items, ok := schema["items"].(map[string]any)
	if !ok || items["$ref"] != "#/definitions/wallet-api_internal_wallet.Token" {
		t.Errorf("native array items = %v, want wallet.Token reference", schema["items"])
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

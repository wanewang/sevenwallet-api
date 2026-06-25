package api

import (
	"net/http"

	"wallet-api/internal/apidocs"
)

// redocHTML renders the embedded spec as a clean static reference via Redoc (CDN).
const redocHTML = `<!DOCTYPE html>
<html>
  <head>
    <title>wallet-api reference</title>
    <meta charset="utf-8"/>
    <meta name="viewport" content="width=device-width, initial-scale=1"/>
  </head>
  <body>
    <redoc spec-url="/openapi.json"></redoc>
    <script src="https://cdn.jsdelivr.net/npm/redoc@2.5.3/bundles/redoc.standalone.js"></script>
  </body>
</html>`

func serveOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(apidocs.SpecJSON)
}

func serveDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(redocHTML))
}

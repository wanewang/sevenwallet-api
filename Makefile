.PHONY: docs docs-check

# Regenerate the OpenAPI spec from handler annotations and copy it for GitHub Pages.
docs:
	go tool swag init -g cmd/server/main.go --output internal/apidocs --outputTypes json,yaml --parseDependency --parseInternal
	mkdir -p docs/api
	cp internal/apidocs/swagger.json docs/api/openapi.json

# CI guard: regenerate and fail if the committed spec is stale.
docs-check: docs
	git diff --exit-code -- internal/apidocs/swagger.json internal/apidocs/swagger.yaml docs/api/openapi.json

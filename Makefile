.PHONY: docs docs-check hooks deploy

# Regenerate the OpenAPI spec from handler annotations and copy it for GitHub Pages.
docs:
	go tool swag init -g cmd/server/main.go --output internal/apidocs --outputTypes json,yaml --parseDependency --parseInternal
	printf '\n' >> internal/apidocs/swagger.json
	mkdir -p docs/api
	cp internal/apidocs/swagger.json docs/api/openapi.json

# CI guard: regenerate and fail if the committed spec is stale.
docs-check: docs
	git diff --exit-code -- internal/apidocs/swagger.json internal/apidocs/swagger.yaml docs/api/openapi.json

# Install git hooks (lefthook) — run once per clone.
hooks:
	go tool lefthook install

# Build from source and deploy to Cloud Run. Secrets (MORALIS/ALCHEMY keys)
# come from Secret Manager; DATABASE_URL and REDIS_URL are passed at deploy
# time so they never live in the repo. REDIS_URL is your Upstash rediss:// URL.
#
# Configure once (export these in your shell or a gitignored env file), then run
# `make deploy`. None of these live in the repo:
#   PROJECT            – GCP project (defaults to your active gcloud project)
#   REGION             – Cloud Run region
#   SERVICE            – Cloud Run service name
#   WALLET_CLOUDSQL    – Cloud SQL connection name (PROJECT:REGION:INSTANCE)
#   WALLET_RUNTIME_SA  – deploy/runtime service account email
#   DATABASE_URL / REDIS_URL – e.g.:
#     export DATABASE_URL='postgres://wallet:PW@/wallet?host=/cloudsql/PROJECT:REGION:INSTANCE'
#     export REDIS_URL='rediss://default:TOKEN@HOST.upstash.io:6379'
PROJECT  ?= $(shell gcloud config get-value project 2>/dev/null)
REGION   ?= us-central1
SERVICE  ?= wallet-api
WALLET_CLOUDSQL   ?=
WALLET_RUNTIME_SA ?=
deploy:
	@test -n "$(WALLET_CLOUDSQL)" || { echo "WALLET_CLOUDSQL is required (PROJECT:REGION:INSTANCE)"; exit 1; }
	@test -n "$(WALLET_RUNTIME_SA)" || { echo "WALLET_RUNTIME_SA is required (deploy service account email)"; exit 1; }
	@test -n "$(DATABASE_URL)" || { echo "DATABASE_URL is required (export it in your shell)"; exit 1; }
	@test -n "$(REDIS_URL)" || { echo "REDIS_URL is required (export your Upstash rediss:// URL)"; exit 1; }
	gcloud run deploy $(SERVICE) --source . --region $(REGION) --project $(PROJECT) \
		--add-cloudsql-instances=$(WALLET_CLOUDSQL) \
		--service-account "$(WALLET_RUNTIME_SA)" \
		--update-secrets="ALCHEMY_API_KEY=alchemy-api-key:1,MORALIS_API_KEY=moralis-api-key:1,DATABASE_URL=database-url:latest,REDIS_URL=redis-url:1" \
		--allow-unauthenticated

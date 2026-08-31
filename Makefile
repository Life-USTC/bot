OPENAPI_SOURCE ?= ../server/public/openapi.generated.json
OPENAPI_SERVER_SHA ?=
BUILD_FLAGS ?=
BUILD_OUTPUT ?=
BUILD_PACKAGE ?= ./...

.PHONY: build dev-e2e generate sync-openapi check-openapi-provenance check-openapi-source print-openapi-commit

build: check-openapi-provenance generate
	go build $(BUILD_FLAGS) $(if $(BUILD_OUTPUT),-o $(BUILD_OUTPUT)) $(BUILD_PACKAGE)

dev-e2e:
	./scripts/dev-e2e.sh

generate:
	go tool oapi-codegen -config api/oapi-codegen.yaml api/openapi.json

sync-openapi:
	./scripts/openapi-contract.sh update "$(OPENAPI_SOURCE)" "$(OPENAPI_SERVER_SHA)"

check-openapi-provenance:
	./scripts/openapi-contract.sh verify

check-openapi-source: check-openapi-provenance
	cmp -s "$(OPENAPI_SOURCE)" api/openapi.json

print-openapi-commit:
	@./scripts/openapi-contract.sh commit

OPENAPI_SOURCE ?= ../server/public/openapi.generated.json

.PHONY: generate sync-openapi check-openapi-sync

generate:
	go tool oapi-codegen -config api/oapi-codegen.yaml api/openapi.json

sync-openapi:
	cp $(OPENAPI_SOURCE) api/openapi.json

check-openapi-sync:
	cmp -s $(OPENAPI_SOURCE) api/openapi.json

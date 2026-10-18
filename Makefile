GO      ?= go
BIN     := bin
SVC_USER  ?= $(shell id -un)
SVC_GROUP ?= $(shell id -gn)

.PHONY: all build web test vet dev systemd clean

all: build

web:
	@if [ -f web/package.json ]; then cd web && npm ci && npm run build; fi

build: web
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN)/mcsman ./cmd/mcsman
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BIN)/mcsctl ./cmd/mcsctl

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

dev: build
	$(BIN)/mcsman --config ./mcsman.yaml

systemd:
	sed -e 's/@USER@/$(SVC_USER)/' -e 's/@GROUP@/$(SVC_GROUP)/' systemd/mcsman.service.in > $(BIN)/mcsman.service

clean:
	rm -rf $(BIN) web/build

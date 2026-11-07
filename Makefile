# Read-only OpenCode Go quota viewer for CLIProxyAPI.
#
# One shared library, one plugin id. The plugin registers no executor and no
# model: it only adds a management page that reports the quota of the OpenCode Go
# keys the host already routes.

MODULE   := github.com/Pheobe-Southwood/cpa-opencodego
VERSION  ?= 1.0.0
GO       ?= go
DIST     := dist
PLUGIN_ID := cpa-opencodego-quota
TARGET   := $(DIST)/$(PLUGIN_ID)-v$(VERSION).so

.PHONY: all build test vet fmt clean list install uninstall

all: build

build: $(TARGET)

$(TARGET): go.mod go.sum $(shell find cmd internal -name '*.go') internal/brand/opencode.svg | $(DIST)
	@echo "building $(PLUGIN_ID) $(VERSION) -> $@"
	CGO_ENABLED=1 $(GO) build -buildmode=c-shared -trimpath \
		-ldflags "-s -w -X main.version=$(VERSION)" \
		-o $@ ./cmd/plugin
	@rm -f $(basename $@).h

$(DIST):
	mkdir -p $(DIST)

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

clean:
	rm -rf $(DIST)

list:
	@echo "$(TARGET)"

# Install copies only the plugin library and prints the config block that has to
# be added to config.yaml. It never edits the host configuration.
install: build
	@bash scripts/install.sh "$(VERSION)"

uninstall:
	@bash scripts/uninstall.sh

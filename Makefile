BINARY := tcp-ip-stack
SRC_DIR := src
UI_DIR := ui
DIST_DIR := dist
WASM := $(UI_DIR)/packet-tracer.wasm
WASM_EXEC := $(UI_DIR)/wasm_exec.js
UI_PORT ?= 8080
GO_BUILD_FLAGS := -trimpath -buildvcs=false
WASM_LDFLAGS := -s -w

all: build

build:
	go build $(GO_BUILD_FLAGS) -o $(BINARY) ./$(SRC_DIR)

run: build
	./$(BINARY)

wasm:
	GOOS=js GOARCH=wasm go build $(GO_BUILD_FLAGS) -ldflags='$(WASM_LDFLAGS)' -o $(WASM) ./$(SRC_DIR)
	gzip -9 -n -k -f $(WASM)
	cp "$$(go env GOROOT)/lib/wasm/wasm_exec.js" $(WASM_EXEC)

dist: wasm
	rm -rf $(DIST_DIR)
	mkdir -p $(DIST_DIR)/scenarios
	cp $(UI_DIR)/index.html $(UI_DIR)/styles.css $(UI_DIR)/app.js $(UI_DIR)/wasm-worker.js $(DIST_DIR)/
	cp $(WASM_EXEC) $(WASM) $(WASM).gz $(DIST_DIR)/
	cp $(UI_DIR)/scenarios/*.yaml $(DIST_DIR)/scenarios/

ui: wasm
	python3 -m http.server $(UI_PORT) -d $(UI_DIR)

test:
	go test ./...

test-integration:
	go test ./$(SRC_DIR) -run '^TestFullSystemIntegration$$'

test-data:
	go test ./$(SRC_DIR) -run '^TestDataTransmission$$'

test-flood:
	go test ./$(SRC_DIR) -run '^TestPacketFlooding$$'

test-stress:
	go test ./$(SRC_DIR) -run '^TestSystemStress$$'

clean:
	rm -f $(BINARY) go-tcp-ip-test $(SRC_DIR)/$(SRC_DIR) $(WASM) $(WASM).gz $(WASM_EXEC)
	rm -rf $(DIST_DIR)

fmt:
	gofmt -w $(SRC_DIR)/*.go

check:
	go vet ./...

dev: fmt check test build wasm

help:
	@echo "Available targets:"
	@echo "  build            Build the native CLI"
	@echo "  run              Build and run the CLI"
	@echo "  wasm             Build stripped and compressed browser assets"
	@echo "  dist             Build the runtime-only Pages distribution"
	@echo "  ui               Build and serve the browser UI on port $(UI_PORT)"
	@echo "  test             Run all tests"
	@echo "  test-integration Run the full-system integration test"
	@echo "  test-data        Run the data-transmission test"
	@echo "  test-flood       Run the packet-flooding test"
	@echo "  test-stress      Run the stress test"
	@echo "  clean            Remove generated binaries and browser assets"
	@echo "  fmt              Format Go source"
	@echo "  check            Run go vet"
	@echo "  dev              Format and validate every build"

.PHONY: all build run wasm dist ui test test-integration test-data test-flood test-stress clean fmt check dev help

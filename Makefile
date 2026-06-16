ENGINE       ?= podman
IMAGE        ?= gamedataemulator:local
REMOTE_IMAGE ?= ghcr.io/firepowerapp/gamedataemulator:latest
RUN_IMAGE    ?= $(IMAGE)
STATS_PORT   ?= 8124
PBP_PORT     ?= 8125

.PHONY: machine build run up down logs pull clean dev test cover

# Start the podman machine (macOS only — no-ops gracefully on Linux)
machine:
	$(ENGINE) machine start 2>/dev/null || true

# Build the local image from the Dockerfile
build:
	$(ENGINE) build -t $(IMAGE) .

# Run the container in the foreground (Ctrl-C to stop)
run: machine
	$(ENGINE) run --rm \
		-p $(STATS_PORT):$(STATS_PORT) \
		-p $(PBP_PORT):$(PBP_PORT) \
		$(RUN_IMAGE)

# Run the container detached
up: machine
	$(ENGINE) run -d --name gamedataemulator \
		-p $(STATS_PORT):$(STATS_PORT) \
		-p $(PBP_PORT):$(PBP_PORT) \
		$(RUN_IMAGE)

# Stop and remove the detached container
down:
	$(ENGINE) stop gamedataemulator 2>/dev/null || true
	$(ENGINE) rm   gamedataemulator 2>/dev/null || true

# Follow logs from the detached container
logs:
	$(ENGINE) logs -f gamedataemulator

# Pull the published image from the registry
# Use: make pull && make up RUN_IMAGE=$(REMOTE_IMAGE)
pull:
	$(ENGINE) pull $(REMOTE_IMAGE)

# Stop container and remove the local image
clean: down
	$(ENGINE) rmi $(IMAGE) 2>/dev/null || true

# Run the server directly (no container — fastest inner loop)
dev:
	go run ./cmd/testserver

# Run the test suite
test:
	go test ./...

# Run tests with coverage report
cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

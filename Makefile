BINARY         ?= git-signing-proxy
IMG            ?= quay.io/alegacy/git-signing-proxy:latest
CONTAINER_TOOL ?= podman
NAMESPACE      ?= git-signing-proxy
KEYS_DIR       ?= $(HOME)/.ssh
LISTEN_ADDR    ?= :8080

export GOTOOLCHAIN ?= go1.25.9

.DEFAULT_GOAL := build

##@ Build

.PHONY: build
build: ## Build the binary
	CGO_ENABLED=0 go build -ldflags='-s -w' -o $(BINARY) .

.PHONY: test
test: ## Run unit tests
	go test -v -race -count=1 ./...

.PHONY: fmt
fmt: ## Format source code
	go fmt ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: vet ## Run all linters (golangci-lint + go vet)
	golangci-lint run ./...

.PHONY: tidy
tidy: ## Tidy and verify go modules
	go mod tidy
	go mod verify

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY)

##@ Container

.PHONY: docker-build
docker-build: ## Build container image
	$(CONTAINER_TOOL) build -t $(IMG) .

.PHONY: docker-push
docker-push: ## Push container image
	$(CONTAINER_TOOL) push $(IMG)

##@ Local

.PHONY: run
run: build ## Run locally (set KEYS_DIR= and LISTEN_ADDR=)
	KEYS_DIR=$(KEYS_DIR) LISTEN_ADDR=$(LISTEN_ADDR) ./$(BINARY)

.PHONY: run-local
run-local: docker-build ## Run in a local container (set KEYS_DIR= to mount keys)
	$(CONTAINER_TOOL) run --rm -it \
		-p 8080:8080 \
		-v $(KEYS_DIR):/etc/signing-keys:ro,Z \
		--name $(BINARY) \
		$(IMG)

.PHONY: stop-local
stop-local: ## Stop the local container
	$(CONTAINER_TOOL) stop $(BINARY) 2>/dev/null || true

##@ Deploy

.PHONY: deploy
deploy: ## Deploy to OpenShift (set IMG to override image)
	oc apply -k deploy/
	oc set image deployment/git-signing-proxy -n $(NAMESPACE) \
		git-signing-proxy=$(IMG)

.PHONY: undeploy
undeploy: ## Remove from OpenShift
	oc delete -k deploy/ --ignore-not-found

.PHONY: create-secret
create-secret: ## Create signing key secret (SECRET_KEY_FILE= SECRET_KEY_ID=)
ifndef SECRET_KEY_FILE
	@echo "Usage: make create-secret SECRET_KEY_FILE=/path/to/key [SECRET_KEY_ID=mykey]"
	@echo ""
	@echo "Examples:"
	@echo "  make create-secret SECRET_KEY_FILE=~/.ssh/id_ed25519 SECRET_KEY_ID=my-ssh-key"
	@echo "  make create-secret SECRET_KEY_FILE=gpg-key.asc SECRET_KEY_ID=my-gpg-key"
	@exit 1
endif
	oc create secret generic git-signing-keys \
		--from-file=$(or $(SECRET_KEY_ID),$(notdir $(SECRET_KEY_FILE)))=$(SECRET_KEY_FILE) \
		--dry-run=client -o yaml | oc apply -f -

##@ Help

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} \
		/^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } \
		/^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) }' $(MAKEFILE_LIST)

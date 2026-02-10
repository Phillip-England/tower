BINARY  := tower
SRC     := ./cmd/tower
DATA    := ./data

.PHONY: build run clean fmt vet test create-user list-users admin-token

## Build & Run ---------------------------------------------------------------

build: ## Build the tower binary
	go build -o $(BINARY) $(SRC)

run: build ## Build and run the server
	./$(BINARY) serve --addr :8080 --data-dir $(DATA)

clean: ## Remove binary and data directory
	rm -f $(BINARY)

## Code Quality --------------------------------------------------------------

fmt: ## Format all Go source files
	go fmt ./...

vet: ## Run go vet
	go vet ./...

test: ## Run all tests
	go test ./...

check: fmt vet test ## Format, vet, and test

## User Management -----------------------------------------------------------

create-user: build ## Create a user (usage: make create-user NAME=Acme)
	./$(BINARY) create-user --name "$(NAME)" --data-dir $(DATA)

list-users: build ## List all users
	./$(BINARY) list-users --data-dir $(DATA)

admin-token: build ## Print the admin token
	./$(BINARY) admin-token --data-dir $(DATA)

## IP Bans -------------------------------------------------------------------

ban-ip: build ## Ban an IP (usage: make ban-ip IP=1.2.3.4 REASON="abuse" DURATION=24h)
	./$(BINARY) ban-ip --ip $(IP) --reason "$(REASON)" --duration $(DURATION) --data-dir $(DATA)

unban-ip: build ## Unban an IP (usage: make unban-ip IP=1.2.3.4)
	./$(BINARY) unban-ip --ip $(IP) --data-dir $(DATA)

list-bans: build ## List all banned IPs
	./$(BINARY) list-bans --data-dir $(DATA)

## Help ----------------------------------------------------------------------

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2}'

.DEFAULT_GOAL := help

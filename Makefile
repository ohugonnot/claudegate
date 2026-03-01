.PHONY: setup build test fmt vet run lint vuln clean

BINARY=bin/claudegate

setup:
	@command -v mise >/dev/null 2>&1 || (curl -fsSL https://mise.run | sh)
	@mise install

build:
	@mkdir -p bin
	@go build -o $(BINARY) ./cmd/claudegate
	@echo "Built $(BINARY)"

test:
	@go test ./... -v -count=1

fmt:
	@go fmt ./...

vet:
	@go vet ./...

run: build
	@./$(BINARY)

lint:
	@go tool golangci-lint run ./...

vuln:
	@go tool govulncheck ./...

clean:
	@rm -rf bin/

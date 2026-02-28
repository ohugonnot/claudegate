.PHONY: setup build test fmt vet run lint clean

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
	@command -v golangci-lint >/dev/null 2>&1 || go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	@golangci-lint run ./...

clean:
	@rm -rf bin/

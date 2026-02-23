.PHONY: build run test clean proto deps docker

# Binary name
BINARY_NAME=hue
MAIN_PATH=./cmd/hue

# Build the application
build:
	go build -o bin/$(BINARY_NAME) $(MAIN_PATH)

# Run the application
run:
	go run $(MAIN_PATH)

# Run tests
test:
	go test -v -race -coverprofile=coverage.out ./...

# View coverage
coverage:
	go tool cover -html=coverage.out

# Clean build artifacts
clean:
	rm -rf bin/
	rm -f coverage.out

# Generate protobuf files
proto:
	protoc --go_out=. --go_opt=paths=source_relative \
		--go-grpc_out=. --go-grpc_opt=paths=source_relative \
		pkg/proto/*.proto

# Install dependencies
deps:
	go mod download
	go mod tidy

# Docker build
docker:
	docker build -t hue:latest -f deployments/docker/Dockerfile .

# Docker run
docker-run:
	docker-compose -f deployments/docker/docker-compose.yml up -d

# Lint
lint:
	golangci-lint run ./...

# Format code
fmt:
	go fmt ./...

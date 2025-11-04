.PHONY: build clean run test fmt vet install

BINARY_NAME=k8s-node-external-ip-watcher
VERSION?=$(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

build:
	go build $(LDFLAGS) -o $(BINARY_NAME)

clean:
	rm -f $(BINARY_NAME)
	go clean

run: build
	./$(BINARY_NAME) --config config.yaml

test:
	go test -v ./...

fmt:
	go fmt ./...

vet:
	go vet ./...

install:
	go install $(LDFLAGS)

build-linux:
	GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BINARY_NAME)-linux-amd64

build-darwin:
	GOOS=darwin GOARCH=amd64 go build $(LDFLAGS) -o $(BINARY_NAME)-darwin-amd64
	GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(BINARY_NAME)-darwin-arm64

build-all: build-linux build-darwin

docker-build:
	docker build -t $(BINARY_NAME):$(VERSION) .

docker-run:
	docker run --rm -v $(PWD)/config.yaml:/config/config.yaml $(BINARY_NAME):$(VERSION)

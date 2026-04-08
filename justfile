# auto-qa justfile

default:
    @just --list

clean:
    rm -R -f ./bin

init: clean
    prek install --install-hooks

run *args:
    go run ./cmd/auto-qa {{args}}

rund *args: buildd
    docker run --rm \
        -v ~/.kube/config:/.kube/config:ro \
        -v ./scenarios:/scenarios:ro \
        -e KUBECONFIG=/.kube/config \
        -e SCENARIOS_ROOT=/scenarios \
        auto-qa {{args}}

build: clean
    go build -o bin/auto-qa ./cmd/auto-qa

buildd:
    docker build -t auto-qa .

test:
    go test -v ./...

# Integration tests require KUBECONFIG to be set
test-integration:
    go test -v ./... -tags integration

# Integration tests require KUBECONFIG to be set
test-all:
    go test -v ./... -tags integration

tidy:
    go mod tidy

lint:
    golangci-lint run ./...

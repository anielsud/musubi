.PHONY: build test vet fmt licenses clean

build:
	go build -o bin/musubi ./cmd/musubi

test:
	go test ./internal/... ./cmd/...

vet:
	go vet ./internal/... ./cmd/...

fmt:
	gofmt -w cmd internal

# Fails if any dependency is under a license that would encumber the code.
licenses:
	go run github.com/google/go-licenses@latest check ./... --disallowed_types=forbidden,restricted

clean:
	rm -rf bin

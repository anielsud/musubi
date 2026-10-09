.PHONY: build test vet fmt licenses eval clean

# FTS5 is compiled into the SQLite driver only with this tag.
TAGS := -tags sqlite_fts5

build:
	go build $(TAGS) -o bin/musubi ./cmd/musubi

test:
	go test $(TAGS) ./internal/... ./cmd/...

vet:
	go vet $(TAGS) ./internal/... ./cmd/...

fmt:
	gofmt -w cmd internal

# Fails if any dependency is under a license that would encumber the code.
licenses:
	go run github.com/google/go-licenses@latest check ./... --disallowed_types=forbidden,restricted

# Synthetic corpus eval with the deterministic embedder: the CI gate.
eval: build
	bin/musubi eval --corpus testdata/synthetic --embed hash

clean:
	rm -rf bin

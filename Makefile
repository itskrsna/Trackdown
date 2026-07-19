.PHONY: build vet test race-test cross-compile-check clean snapshot

BINARY := trackdown
CMD := ./cmd/trackdown

build:
	go build -o $(BINARY) $(CMD)

vet:
	go vet ./...

test:
	go test ./...

# Go's race detector needs CGO even for a pure-Go project like this one --
# . CI's Linux/macOS runners have a C compiler
# preinstalled; on Windows you'll need one too.
race-test:
	CGO_ENABLED=1 go test -race ./...

# Sanity-checks that the codebase cross-compiles cleanly for every platform
# this project targets, without actually producing binaries.
cross-compile-check:
	GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
	GOOS=darwin GOARCH=amd64 go build -o /dev/null ./...
	GOOS=linux GOARCH=amd64 go build -o /dev/null ./...
	GOOS=windows GOARCH=amd64 go build -o /dev/null ./...

clean:
	rm -f $(BINARY) $(BINARY).exe
	rm -rf dist

# Builds real cross-platform binaries into dist/ via goreleaser, entirely
# locally -- --snapshot never touches a remote, never tags, never publishes.
snapshot:
	goreleaser build --snapshot --clean

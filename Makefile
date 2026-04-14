.PHONY: build test coverage coverage-html clean

# Build the binary
build:
	go build .

# Build without optimizations (debug)
build.debug:
	go build -tags=debug -o remote-control.debug

build.android:
	GOOS=android GOARCH=arm64 go build -o remote-control-android .

docker:
	docker build . -t remote-control

# Run all tests with race detection.
test:
	go test ./... -race -count=1 -timeout 120s

# Generate a per-function coverage summary.
# -coverpkg=./... instruments all packages across all test binaries,
# giving accurate cross-package coverage (e.g. api tests covering session code).
test.coverage:
	go test ./... -race -count=1 -timeout 120s \
		-coverprofile=coverage.out -covermode=atomic \
		-coverpkg=./...
	go tool cover -func=coverage.out

# Build an HTML coverage report and open it.
test.coverage-html: test.coverage
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report written to coverage.html"

clean:
	rm -f coverage.out coverage.html

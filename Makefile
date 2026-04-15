REGISTRY?=us.icr.io/ghart
IMAGE_NAME?=${REGISTRY}/remote-control:$(shell ./scripts/version.sh)

# Build the binary
.PHONY: build
build:
	go build .

# Build without optimizations (debug)
.PHONY: build.debug
build.debug:
	go build -tags=debug -o remote-control.debug

.PHONY: build.android
build.android:
	GOOS=android GOARCH=arm64 go build -o remote-control-android .

.PHONY: docker
docker:
	docker build . -t remote-control --load

.PHONY: docker.release
docker.release:
	docker buildx build --platform linux/arm64,linux/amd64 . -t ${IMAGE_NAME} --push

# Run all tests with race detection.
.PHONY: test
test:
	go test ./... -race -count=1 -timeout 120s

# Generate a per-function coverage summary.
# -coverpkg=./... instruments all packages across all test binaries,
# giving accurate cross-package coverage (e.g. api tests covering session code).
.PHONY: test.coverage
test.coverage:
	go test ./... -race -count=1 -timeout 120s \
		-coverprofile=coverage.out -covermode=atomic \
		-coverpkg=./...
	go tool cover -func=coverage.out

# Build an HTML coverage report and open it.
.PHONY: test.coverage-html
test.coverage-html: test.coverage
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report written to coverage.html"

.PHONY: clean
clean:
	rm -f coverage.out coverage.html

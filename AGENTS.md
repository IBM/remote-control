## Development Requirements

* Always make sure to read README.md before starting on a project so you have an up-to-date view of the high-level project architecture and usage
* All architectural plans or proposals should be placed in docs/agent-arch
* Agents should _never_ create git commits as that is the responsibility of the developer. Agents may suggest that the developer commit the work and may suggest commit messages, but they may not run `git commit`.
* Code should NEVER include trailing whitespace (lines that end in any whitespace before the newline character)

## Testing

* To run the full test suite, use `make test` which runs `go test` with race detection enabled
* Non-test code should never contain code paths that are only to support tests
* All test packages should include the following snippets in one `*_test.go` file to use the shared main entrypoint:

```go
import (
    ...
	testmain "github.com/gabe-l-hart/remote-control/test"
    ...
)
```

```go
func TestMain(m *testing.M) {
	testmain.TestMain(m)
}
```

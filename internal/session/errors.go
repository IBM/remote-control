package session

import "fmt"

type notFoundError struct {
	id string
}

func (e *notFoundError) Error() string {
	return fmt.Sprintf("entry not found: %s", e.id)
}

func errNotFound(id string) error {
	return &notFoundError{id: id}
}

// IsNotFound reports whether err is a not-found error.
func IsNotFound(err error) bool {
	_, ok := err.(*notFoundError)
	return ok
}

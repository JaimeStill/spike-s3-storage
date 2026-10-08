package cli

import "fmt"

// UsageError is an error in how a command was invoked rather than in what it
// did, such as a wrong number of arguments. [Run] recognises one anywhere in
// a returned error's chain, reports it with the command's usage, and returns
// ExitUsage; every other error returns ExitFailure.
type UsageError struct {
	Err error
}

// Usagef returns a [UsageError] whose message is formatted as fmt.Errorf
// formats it, so %w wraps a cause.
func Usagef(format string, args ...any) error {
	return &UsageError{Err: fmt.Errorf(format, args...)}
}

// Error returns the message of the wrapped error.
func (e *UsageError) Error() string {
	return e.Err.Error()
}

// Unwrap returns the wrapped error.
func (e *UsageError) Unwrap() error {
	return e.Err
}

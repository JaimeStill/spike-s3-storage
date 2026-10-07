// Package output holds the layouts a command's result is rendered in, so
// every command family prints the same way. [Table] writes rows as aligned
// columns under a header, and [Record] writes one row as aligned label and
// value lines, each a [Field]. [Count] renders a count with the noun that
// agrees with it, for the counts a one-line success states. What a result says is its domain's: each
// domain package renders its own results over these layouts in its own
// output.go, and a one-line success is a plain fmt.Fprintf. Failures are
// not rendered here: a command returns its error and package cli reports
// it.
//
// The package imports only the standard library.
package output

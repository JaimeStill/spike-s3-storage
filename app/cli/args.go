package cli

// NoArgs is an Args validator that rejects any positional argument.
func NoArgs(args []string) error {
	if len(args) > 0 {
		return Usagef("accepts no arguments, got %d", len(args))
	}
	return nil
}

// ExactArgs returns an Args validator that accepts exactly n positional
// arguments.
func ExactArgs(n int) func(args []string) error {
	return func(args []string) error {
		if len(args) != n {
			return Usagef("accepts %d %s, got %d", n, plural(n, "argument"), len(args))
		}
		return nil
	}
}

// plural returns word, with an "s" unless n is 1.
func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

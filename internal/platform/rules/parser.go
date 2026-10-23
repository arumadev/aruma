package rules

// parse reads a rules file into its tree, and reports every syntax error it finds, recovering
// at ; and }. Nesting deeper than lim.ExpressionDepth and member chains longer than
// lim.MemberChain are errors ("Expression is too complex to evaluate safely."). It returns a
// nil file only when nothing could be read. S1-A replaces this stub.
func parse(name, src string, lim Limits) (*file, []Issue) {
	panic("S1-A: parse")
}

// Command protomerge checks the merge rule of third_party/README.md: googleapis is the base, and
// a field (or enum value) that only firebase-js-sdk's copy of a proto has is added to the
// googleapis copy with its exact number. It fails when a js-sdk field is missing from the
// googleapis copy, has another number or type there, or collides with a field, value or reserved
// number googleapis gives to something else. `make proto` runs it before generating, and its test
// runs with every other.
//
//	go run ./tools/protomerge [repo root]
package main

import (
	"fmt"
	"os"
)

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	problems, err := Check(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "protomerge:", err)
		os.Exit(2)
	}
	for _, p := range problems {
		fmt.Fprintln(os.Stderr, "protomerge:", p)
	}
	if len(problems) > 0 {
		os.Exit(1)
	}
}

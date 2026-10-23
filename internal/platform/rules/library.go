package rules

// The library's two dispatch tables: the functions of each type, and the global and namespaced
// functions (int(), float(), string(), path(), math.*, hashing.*, latlng.value, timestamp.*,
// duration.*, debug()). The interpreter reaches them through callMethod and callFunction. The
// lookups (get, exists, getAfter, existsAfter, firestore.*) are not here: lookup.go has them.
// Errors are an *evalError or an *abort, nothing else. S1-B replaces the two stubs below.

// libraryNamespaces are the namespaces of the rules.* reference whose functions callFunction
// serves. firestore (Storage's lookups) is not one of them: lookup.go has it.
var libraryNamespaces = map[string]bool{
	"duration": true, "hashing": true, "latlng": true, "math": true, "timestamp": true,
}

// libraryFunctions are the global and namespaced functions of the rules.* reference
// (firebase.google.com/docs/reference/rules/rules), by the name callFunction takes. The checker
// (S1-C) resolves calls with isLibraryFunction; S1-B keeps the list in step with callFunction
// and may extend it from the oracle.
var libraryFunctions = map[string]bool{
	"debug": true, "float": true, "int": true, "path": true, "string": true,
	"duration.abs": true, "duration.time": true, "duration.value": true,
	"hashing.crc32": true, "hashing.crc32c": true, "hashing.md5": true, "hashing.sha256": true,
	"math.abs": true, "math.ceil": true, "math.floor": true, "math.isInfinite": true,
	"math.isNaN": true, "math.pow": true, "math.round": true, "math.sqrt": true,
	"latlng.value": true, "timestamp.date": true, "timestamp.value": true,
}

// isLibraryFunction reports whether name ("int", "math.abs") is a global or namespaced library
// function. The same in both services.
func isLibraryFunction(name string) bool { return libraryFunctions[name] }

// isLibraryNamespace reports whether name ("math") is a namespace of library functions.
func isLibraryNamespace(name string) bool { return libraryNamespaces[name] }

// callMethod calls recv.name(args).
func callMethod(m *meter, recv Value, name string, args []Value) (Value, error) {
	panic("S1-B: callMethod")
}

// callFunction calls the global or namespaced function name ("int", "math.abs") with args.
func callFunction(m *meter, name string, args []Value) (Value, error) {
	panic("S1-B: callFunction")
}

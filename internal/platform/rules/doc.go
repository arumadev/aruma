// Package rules is the security rules language of Cloud Firestore and Cloud Storage for
// Firebase: one language for the services cloud.firestore and firebase.storage, compiled when
// rules are published and evaluated on every request (ADR-021).
//
// The engine is written by hand on the standard library alone: a lexer, a recursive-descent
// parser, a checker that flattens the match blocks, and a tree-walking interpreter in which an
// error is a value until && or || absorbs it. The services know nothing of its inside: each
// builds its "request" and "resource" values with the constructors in value.go, names the
// method and the path segments, and supplies the Documents its lookups read (rules.NoDocuments{}
// where Firestore cannot be read). internal/app makes the one Inflight that caps the memory of
// every evaluation in the process.
//
// Behaviour follows, in order: rules deployed on a real project and requests from Firebase's
// SDKs; the Rules Test API (firebaserules.googleapis.com, projects.test) for the language
// alone; the official emulator as a black box. The cases live in test/oracle/rules with their
// source, date and version. The references are Firebase's documentation (rules-language,
// rules-conditions, rules-structure, the rules.* reference), hidane's ADR 0005 and its
// measurements of the emulator, and Pyric's recordings of the Rules Test API, read as ideas,
// never as code (ADR-021).
//
// What the package does not do: it reports fewer warnings than the emulator (only an unknown
// function and an unused one; the emulator's type warnings are not ported), and it keeps limits
// of its own that Firebase does not have, which docs/compat/rules-v0.3.md lists. A Constraint or
// an Unknown value, reserved for Firestore's list queries (F5), is an error wherever it meets an
// operation, so the engine never allows what it does not understand.
//
// The package imports nothing from the rest of Aruma (archtest).
package rules

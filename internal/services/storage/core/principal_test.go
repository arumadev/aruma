package core

import (
	"encoding/json"
	"testing"
)

// An upload session keeps its principal's claims as the token wrote them: a whole number beyond
// 2^53 is still that int in the rules when the upload resumes (deployed rules read
// 9007199254740993 exactly: S7-R, test/oracle/rules/production/deployed-firestore.json).
func TestSessionPrincipalKeepsClaimNumbers(t *testing.T) {
	in := Principal{Kind: PrincipalUser, UID: "u1", Token: map[string]any{
		"n": json.Number("9007199254740993"), "f": json.Number("2.5"),
		"m": map[string]any{"n": json.Number("-9007199254740993")},
	}}
	kind, raw, err := encodePrincipal(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := decodePrincipal(kind, raw)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := out.Token["m"].(map[string]any)
	if out.Kind != PrincipalUser || out.UID != "u1" || out.Token["n"] != json.Number("9007199254740993") ||
		out.Token["f"] != json.Number("2.5") || m["n"] != json.Number("-9007199254740993") {
		t.Fatalf("decoded %+v from %s", out, raw)
	}
}

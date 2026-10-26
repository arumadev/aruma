package core

import (
	"errors"
	"testing"

	"github.com/arumadev/aruma/internal/services/documents/value"
)

// A name that is not UTF-8 is never stored, whatever the transport let through: its key would
// not read back (0xFF 0x01 escapes to FF 00 01, which ends a segment).
func TestApplyRefusesNamesNotUTF8(t *testing.T) {
	p := newPlan([]Write{{Kind: WriteUpdate, Path: Path{"c", "\xff\x01"}, Fields: value.Map()}})
	err := p.apply(DB{ProjectID: "p", ID: "(default)"}, map[string]*lockedDoc{})
	if ce, ok := errors.AsType[*Error](err); !ok || ce.Code != InvalidArgument {
		t.Fatalf("apply: %v", err)
	}
}

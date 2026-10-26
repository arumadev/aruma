// Package protocheck holds the index package's hand-encoded console payload to the generated
// google.firestore.admin.v1.Index. The index package imports Go's standard library alone (the
// archtest's stdlibOnly, test files included), so this cross-check lives beside it, where the
// generated protos may be imported.
package protocheck

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/arumadev/aruma/internal/services/documents/index"
	"github.com/arumadev/aruma/internal/services/documents/wire/pb/adminpb"
)

func order(o adminpb.Index_IndexField_Order) *adminpb.Index_IndexField_Order_ {
	return &adminpb.Index_IndexField_Order_{Order: o}
}

// TestEncodeIndexMatchesGeneratedProto: EncodeIndex gives the bytes the generated code
// marshals deterministically, for a composite, an exemption and an array-contains field.
func TestEncodeIndexMatchesGeneratedProto(t *testing.T) {
	const project, database = "p", index.DefaultDatabase
	field := func(path string, mode index.FieldMode) index.IndexField {
		return index.IndexField{Path: path, Mode: mode}
	}
	cases := []struct {
		name string
		r    index.Required
		want *adminpb.Index
	}{
		{"composite", index.Required{Index: index.Index{CollectionGroup: "c", Scope: index.ScopeCollection,
			Fields: []index.IndexField{field("a", index.Ascending), field("b.`x y`", index.Descending), field(index.NameField, index.Descending)}}},
			&adminpb.Index{
				Name:       "projects/p/databases/(default)/collectionGroups/c/indexes/_",
				QueryScope: adminpb.Index_COLLECTION,
				Fields: []*adminpb.Index_IndexField{
					{FieldPath: "a", ValueMode: order(adminpb.Index_IndexField_ASCENDING)},
					{FieldPath: "b.`x y`", ValueMode: order(adminpb.Index_IndexField_DESCENDING)},
					{FieldPath: "__name__", ValueMode: order(adminpb.Index_IndexField_DESCENDING)},
				},
			}},
		{"array-contains", index.Required{Index: index.Index{CollectionGroup: "c", Scope: index.ScopeCollectionGroup,
			Fields: []index.IndexField{field("tags", index.Contains), field("d", index.Ascending), field(index.NameField, index.Ascending)}}},
			&adminpb.Index{
				Name:       "projects/p/databases/(default)/collectionGroups/c/indexes/_",
				QueryScope: adminpb.Index_COLLECTION_GROUP,
				Fields: []*adminpb.Index_IndexField{
					{FieldPath: "tags", ValueMode: &adminpb.Index_IndexField_ArrayConfig_{ArrayConfig: adminpb.Index_IndexField_CONTAINS}},
					{FieldPath: "d", ValueMode: order(adminpb.Index_IndexField_ASCENDING)},
					{FieldPath: "__name__", ValueMode: order(adminpb.Index_IndexField_ASCENDING)},
				},
			}},
		{"exemption", index.Required{Exemption: true, Index: index.Index{CollectionGroup: "c", Scope: index.ScopeCollectionGroup,
			Fields: []index.IndexField{field("d", index.Descending)}}},
			&adminpb.Index{
				Name:       "projects/p/databases/(default)/collectionGroups/c/fields/d",
				QueryScope: adminpb.Index_COLLECTION_GROUP,
				Fields:     []*adminpb.Index_IndexField{{FieldPath: "d", ValueMode: order(adminpb.Index_IndexField_DESCENDING)}},
			}},
	}
	for _, c := range cases {
		want, err := proto.MarshalOptions{Deterministic: true}.Marshal(c.want)
		if err != nil {
			t.Fatal(err)
		}
		if got := index.EncodeIndex(project, database, c.r); !bytes.Equal(got, want) {
			t.Errorf("%s: EncodeIndex = %x, generated proto %x", c.name, got, want)
		}
	}
}

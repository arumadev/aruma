package wire

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// DefaultDatabase is the id of a project's default database.
const DefaultDatabase = "(default)"

// The limits production holds a document name to (f0-errors.json).
const (
	// MaxIDBytes bounds each collection and document id: 1500 bytes pass, 1501 fail with "The
	// key path element name is longer than 1500 bytes." (id_1500_bytes, id_1501_bytes).
	MaxIDBytes = 1500
	// MaxKeyElements bounds a name's collection/document pairs: a document under 99 nested
	// subcollections passes, one under 100 fails with "Key path is too long. Cannot exceed 100
	// elements." (depth_99, depth_100).
	MaxKeyElements = 100
	// MaxNameBytes bounds a name measured as value.NameSize measures it (each id plus 1, plus
	// 16): 6144 passes this check, 6145 fails with "The document name is longer than 6144
	// bytes." (name_bytes_*). A name of 6144 still fails later, on its index entry.
	MaxNameBytes = 6144
)

// Database is a database by project id and database id.
type Database struct {
	Project string
	ID      string
}

// Name is the database's resource name, projects/{p}/databases/{d}.
func (d Database) Name() string { return "projects/" + d.Project + "/databases/" + d.ID }

// DocumentsName is the root of the database's documents, projects/{p}/databases/{d}/documents:
// the parent of its top-level collections.
func (d Database) DocumentsName() string { return d.Name() + "/documents" }

func (d Database) String() string { return d.Name() }

// Name is a resource name below a database's documents: the root itself (no segments), a
// collection (an odd number: collection id, document id, …, collection id) or a document (an
// even number, at least two).
type Name struct {
	Database Database
	// Path holds the collection and document ids below documents/, in order.
	Path []string
}

// IsRoot reports whether n is the documents root.
func (n Name) IsRoot() bool { return len(n.Path) == 0 }

// IsDocument reports whether n names a document.
func (n Name) IsDocument() bool { return len(n.Path) > 0 && len(n.Path)%2 == 0 }

// IsCollection reports whether n names a collection.
func (n Name) IsCollection() bool { return len(n.Path)%2 == 1 }

// ID is the last id of n's path: a document's id or a collection's id; "" for the root.
func (n Name) ID() string {
	if len(n.Path) == 0 {
		return ""
	}
	return n.Path[len(n.Path)-1]
}

// Parent is the name one level up: a document's collection, a collection's parent document or
// the root. The root's parent is the root.
func (n Name) Parent() Name {
	if len(n.Path) == 0 {
		return n
	}
	return Name{n.Database, n.Path[: len(n.Path)-1 : len(n.Path)-1]}
}

// Child is the name of id below n.
func (n Name) Child(id string) Name {
	p := make([]string, len(n.Path), len(n.Path)+1)
	copy(p, n.Path)
	return Name{n.Database, append(p, id)}
}

// RelativePath is n's path below documents/, "c/a/sub/x"; "" for the root.
func (n Name) RelativePath() string { return strings.Join(n.Path, "/") }

// String is n's full resource name.
func (n Name) String() string {
	if len(n.Path) == 0 {
		return n.Database.DocumentsName()
	}
	return n.Database.DocumentsName() + "/" + n.RelativePath()
}

// What a name must be: the kind a parser accepts.
type nameKind uint8

const (
	kindDocument nameKind = iota // a document
	kindParent                   // the root or a document: the parent of a collection
	kindAny                      // the root, a collection or a document
)

// accepts reports whether a name of n ids is of kind k.
func (k nameKind) accepts(n int) bool {
	switch k {
	case kindDocument:
		return n > 0 && n%2 == 0
	case kindParent:
		return n%2 == 0
	}
	return true
}

// The nouns production's name messages start with: "Document name" for a document's name
// (f1-batch-get.json relative_name, collection_name; f1-delete.json collection_path),
// "Document parent name" for a parent (f1-list-collection-ids.json collection_parent) and,
// oddly, for a reference value (values-validate.json ref.*).
const (
	nounDocument  = "Document name"
	nounParent    = "Document parent name"
	nounReference = "Document parent name"
	nounDatabase  = "Database name" // provisional
)

// ParseDocumentName parses a document's full resource name,
// projects/{p}/databases/{d}/documents/{collection}/{id}[/{collection}/{id}…], with production's
// rules and messages: see ParseName.
func ParseDocumentName(s string) (Name, error) { return parseName(s, kindDocument, nounDocument) }

// ParseReference parses a reference value: a document name, held to the rules of
// ParseDocumentName (values-validate.json, ref.*; values-order.json, r-*). The project and
// database are not bound: a reference to another project or database is data, which production
// stores, orders and matches.
func ParseReference(s string) (Name, error) { return parseName(s, kindDocument, nounReference) }

// ParseParentName parses the parent of a collection: the documents root
// (projects/{p}/databases/{d}/documents) or a document, as RunQuery, RunAggregationQuery,
// PartitionQuery, ListDocuments, ListCollectionIds, CreateDocument and Listen's query targets
// carry it.
func ParseParentName(s string) (Name, error) { return parseName(s, kindParent, nounParent) }

// ParseName parses a name below a database's documents: the root, a collection or a document.
// The rules are production's, as recorded:
//
//   - the form is read left to right; where it breaks, `Document name "{s}" lacks {what} at
//     index {i}.`, {what} being "projects", "/", "databases", "documents" in quotes, or "a
//     resource id" for an empty id, and {i} the offset in UTF-16 units (Java's); a parent and a
//     reference say "Document parent name";
//   - the database id is (default) or 4 to 63 lowercase letters, digits and hyphens, starting
//     with a letter and ending with a letter or digit, else `Illegal string "{d}" in database.`;
//   - an id of the form __…__ is reserved, and so are . and ..: `Resource id "{id}" is invalid
//     because it is reserved.` (f1-create.json dot_id); except a document id __id{LONG}__, the
//     numeric id, where LONG is a 64-bit integer written canonically; a document id __id…__
//     otherwise gets `A long document ID string must be formatted like '__id{LONG}__', where
//     LONG is a 64-bit integer. Found: '{id}'.` (values-order.json, r-c-id-*);
//   - then the key's limits: MaxIDBytes, MaxKeyElements, MaxNameBytes.
//
// Every error is INVALID_ARGUMENT.
func ParseName(s string) (Name, error) { return parseName(s, kindAny, nounDocument) }

// ParseDatabaseName parses a database's resource name, projects/{p}/databases/{d}, as the
// database field of a request and google-cloud-resource-prefix carry it.
func ParseDatabaseName(s string) (Database, error) {
	p := nameParser{s: s, noun: nounDatabase}
	db, err := p.database()
	if err != nil {
		return Database{}, err
	}
	if p.pos != len(s) {
		return Database{}, p.trailing()
	}
	return db, nil
}

func parseName(s string, kind nameKind, noun string) (Name, error) {
	p := nameParser{s: s, noun: noun}
	db, err := p.database()
	if err != nil {
		return Name{}, err
	}
	if err := p.literal("/"); err != nil {
		return Name{}, err
	}
	if err := p.literal("documents"); err != nil {
		return Name{}, err
	}
	var path []string
	for p.pos < len(s) || !kind.accepts(len(path)) {
		if err := p.literal("/"); err != nil {
			return Name{}, err
		}
		id, err := p.id()
		if err != nil {
			return Name{}, err
		}
		if err := checkID(id, len(path)%2 == 1); err != nil {
			return Name{}, err
		}
		path = append(path, id)
	}
	if err := checkKey(path); err != nil {
		return Name{}, err
	}
	return Name{db, path}, nil
}

// CheckID checks an id given on its own: CreateDocument's document_id, or a collection id. It
// may not hold a slash (`Resource id "a/b" is invalid because it contains "/".`,
// f1-create.json slash_id) and is held to ParseName's id rules; document says whether it is a
// document's id. The empty id is the caller's to handle (CreateDocument picks one).
func CheckID(id string, document bool) error {
	if strings.Contains(id, "/") {
		return InvalidArgument(`Resource id "` + id + `" is invalid because it contains "/".`)
	}
	if err := checkID(id, document); err != nil {
		return err
	}
	if len(id) > MaxIDBytes {
		return InvalidArgument("The key path element name is longer than 1500 bytes.")
	}
	return nil
}

// checkID applies the reserved-id rules to a collection id or, when document is set, a
// document id.
func checkID(id string, document bool) error {
	if !reservedID(id) {
		return nil
	}
	if document && strings.HasPrefix(id, "__id") && strings.HasSuffix(id, "__") {
		digits := id[len("__id") : len(id)-len("__")]
		if n, err := strconv.ParseInt(digits, 10, 64); err == nil && strconv.FormatInt(n, 10) == digits {
			return nil
		}
		return InvalidArgument("A long document ID string must be formatted like '__id{LONG}__', where LONG is a 64-bit integer. Found: '" + id + "'.")
	}
	return InvalidArgument(`Resource id "` + id + `" is invalid because it is reserved.`)
}

// reservedID reports whether id is reserved: . or .., or the form __.*__, read as value reads a
// reserved field name (two underscores, at least one character, two underscores).
func reservedID(id string) bool {
	return id == "." || id == ".." || len(id) >= 5 && strings.HasPrefix(id, "__") && strings.HasSuffix(id, "__")
}

// checkKey applies the key's limits, in production's order as far as the recordings show it.
func checkKey(path []string) error {
	for _, id := range path {
		if len(id) > MaxIDBytes {
			return InvalidArgument("The key path element name is longer than 1500 bytes.")
		}
	}
	if (len(path)+1)/2 > MaxKeyElements {
		return InvalidArgument("Key path is too long. Cannot exceed 100 elements.")
	}
	if len(path) > 0 && nameSize(path) > MaxNameBytes {
		return InvalidArgument("The document name is longer than 6144 bytes.")
	}
	return nil
}

// nameSize is value.NameSize of a path: each id's length plus 1, plus 16.
func nameSize(path []string) int {
	n := 16
	for _, id := range path {
		n += len(id) + 1
	}
	return n
}

// validDatabaseID reports whether id is a database id production accepts in a name.
func validDatabaseID(id string) bool {
	if id == DefaultDatabase {
		return true
	}
	if len(id) < 4 || len(id) > 63 || !isLower(id[0]) {
		return false
	}
	if last := id[len(id)-1]; !isLower(last) && !isDigit(last) {
		return false
	}
	for i := range len(id) {
		if c := id[i]; !isLower(c) && !isDigit(c) && c != '-' {
			return false
		}
	}
	return true
}

func isLower(c byte) bool { return c >= 'a' && c <= 'z' }
func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// nameParser reads a resource name left to right, reporting where it breaks as production
// does.
type nameParser struct {
	s    string
	pos  int // byte offset
	noun string
}

// database reads projects/{p}/databases/{d}.
func (p *nameParser) database() (Database, error) {
	if err := p.literal("projects"); err != nil {
		return Database{}, err
	}
	if err := p.literal("/"); err != nil {
		return Database{}, err
	}
	project, err := p.id()
	if err != nil {
		return Database{}, err
	}
	if err := p.literal("/"); err != nil {
		return Database{}, err
	}
	if err := p.literal("databases"); err != nil {
		return Database{}, err
	}
	if err := p.literal("/"); err != nil {
		return Database{}, err
	}
	id, err := p.id()
	if err != nil {
		return Database{}, err
	}
	if !validDatabaseID(id) {
		return Database{}, InvalidArgument(`Illegal string "` + id + `" in database.`)
	}
	return Database{project, id}, nil
}

func (p *nameParser) literal(lit string) error {
	if strings.HasPrefix(p.s[p.pos:], lit) {
		p.pos += len(lit)
		return nil
	}
	return p.lacks(`"` + lit + `"`)
}

// id reads one id: everything up to the next slash, which must not be empty.
func (p *nameParser) id() (string, error) {
	rest := p.s[p.pos:]
	n := strings.IndexByte(rest, '/')
	if n < 0 {
		n = len(rest)
	}
	if n == 0 {
		return "", p.lacks("a resource id")
	}
	p.pos += n
	return rest[:n], nil
}

func (p *nameParser) lacks(what string) error {
	return InvalidArgument(p.noun + ` "` + p.s + `" lacks ` + what + " at index " + strconv.Itoa(utf16Len(p.s[:p.pos])) + ".")
}

// trailing is the error for a name that goes on where it should end (provisional text).
func (p *nameParser) trailing() error {
	return InvalidArgument(p.noun + ` "` + p.s + `" has unexpected text at index ` + strconv.Itoa(utf16Len(p.s[:p.pos])) + ".")
}

// utf16Len is the length of s in UTF-16 code units, as Java counts a string's index; a byte
// that is not UTF-8 counts one, as its replacement character would.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 && r != utf8.RuneError {
			n += 2
		} else {
			n++
		}
	}
	return n
}

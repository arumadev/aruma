// Package wire is the Document Database's wire layer, shared by its gRPC (Connect) and REST
// surfaces (ADR-027). It holds no handler: the transports call it.
//
//   - names.go parses and formats resource names (databases, the documents root, collections,
//     documents, parents) with production's rules and messages; bind.go checks that every name a
//     request carries belongs to the project and database the caller may use (ADR-027 §2.2).
//   - convert.go converts between the generated protos (wire/pb) and package value, losslessly.
//   - status.go is the one error form, Error: a code, production's message and google.rpc.Status
//     details, rendered as a Connect error or as REST's HTTP status and Google JSON body (an
//     array on the streaming methods); errors.go holds the errors whose texts production fixed.
//   - protojson.go reads REST bodies and query parameters as Google's front end does, with its
//     error messages, and writes proto3 JSON answers.
//
// Every message here is production's, byte for byte, as recorded under
// test/oracle/documents/corpus; the few that were not recorded are marked provisional where they
// are written.
package wire

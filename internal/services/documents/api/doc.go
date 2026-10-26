// Package api is the Document Database's service layer: the one place every request of every
// surface passes through (ADR-027 §2.2, ADR-029, ADR-025 §8). The gRPC (Connect) and REST
// transports decode a request into its google.firestore.v1 message and hand it here with how it
// arrived (Call); for every request, in this order, the service
//
//   - resolves the caller from its credential and transport (authn.Documents.Resolve);
//   - binds every resource name the request carries to one database (wire.Bind), so a name of
//     another project or database is refused with production's text before any work;
//   - admits the caller to that database's project (authn.Documents.Admit);
//   - refuses whoever is not an admin until security rules arrive in F5 ("Missing or
//     insufficient permissions."), unless documents.dev_open_rules opens the database to this
//     machine;
//   - checks the request's own fields as production does, and calls core;
//   - maps every failure to a *wire.Error with production's text, which each transport renders
//     (core knows no wire form, so the mapping lives here).
//
// It answers with the response messages themselves, which the transports encode.
//
// The files:
//
//   - service.go: Service, Call and the admission above, and the error mapping;
//   - documents.go: the F1 methods (GetDocument, BatchGetDocuments, ListDocuments,
//     CreateDocument, UpdateDocument, DeleteDocument, ListCollectionIds);
//   - fields.go: masks, the values a write stores, and document conversion;
//   - order.go and pagetoken.go: ListDocuments' order_by and both listings' page tokens.
package api

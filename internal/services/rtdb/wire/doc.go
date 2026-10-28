// Package wire is the Firebase Realtime Database wire protocol, version 5: the frames a
// WebSocket carries, the envelopes inside them, the client's actions and the server's
// replies and pushes. It knows nothing of the network or of the data model, so it is tested
// on recorded frames alone.
//
// Google publishes no specification of this protocol. The reference is the SDKs' code, read
// at firebase-js-sdk d7bb0e16f2 (packages/database/src/realtime), firebase-android-sdk
// dcc8d0eb52 and firebase-ios-sdk dc5821fd8c, and the official emulator 4.11.2 observed from
// the outside. Frames the
// emulator sent are kept under testdata/golden with their provenance.
//
// The shape of every message:
//
//	control  {"t":"c","d":{"t":"<type>","d":<payload>}}       hello h, ping p, pong o, ...
//	request  {"t":"d","d":{"r":<n>,"a":"<action>","b":{...}}} client to server
//	reply    {"t":"d","d":{"r":<n>,"b":{"s":"<status>","d":<data>}}}
//	push     {"t":"d","d":{"a":"<action>","b":{...}}}          server to client, no r
package wire

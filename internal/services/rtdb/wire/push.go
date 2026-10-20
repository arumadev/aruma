package wire

import "encoding/json"

// dataPush is the body of d and m. Path and Data are always present: iOS drops a push that
// lacks either (FPersistentConnection.m:1091-1094), so a deletion carries "d":null.
type dataPush struct {
	Path string          `json:"p"`
	Data json.RawMessage `json:"d"`
	Tag  *int64          `json:"t,omitempty"`
}

// EncodeDataPush replaces the data at path for the listener tagged tag (nil for a default
// listen). data is JSON in export format; nil means null.
func EncodeDataPush(path string, data json.RawMessage, tag *int64) []byte {
	return encodePush(PushData, dataPush{Path: PushPath(path), Data: orNull(data), Tag: tag})
}

// EncodeMergePush merges the children in data, an object keyed by relative paths, into path.
// A null child deletes it.
func EncodeMergePush(path string, data json.RawMessage, tag *int64) []byte {
	return encodePush(PushMerge, dataPush{Path: PushPath(path), Data: orNull(data), Tag: tag})
}

// EncodeListenRevoked cancels the listen at path with query q (nil for a default listen); the
// emulator sends "q":null then. JS removes the matching listen and raises permission_denied;
// Android and iOS remove every listen at path.
func EncodeListenRevoked(path string, q *Query) []byte {
	return encodePush(PushListenRevoked, struct {
		Path  string `json:"p"`
		Query *Query `json:"q"`
	}{PushPath(path), q})
}

// EncodeAuthRevoked tells the client its credential no longer holds. The SDKs drop the token,
// refresh it and reconnect (PersistentConnection.ts:985-1004).
func EncodeAuthRevoked(status, reason string) []byte {
	return encodePush(PushAuthRevoked, Reply{Status: status, Data: reason})
}

func encodePush(action string, body any) []byte {
	return encode(envelope2{T: TypeData, D: struct {
		A string `json:"a"`
		B any    `json:"b"`
	}{action, body}})
}

func orNull(d json.RawMessage) json.RawMessage {
	if len(d) == 0 {
		return json.RawMessage("null")
	}
	return d
}

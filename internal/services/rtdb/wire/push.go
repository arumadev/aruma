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

// EncodeListenRevoked cancels the listen at path with query q (nil for a default listen).
// It names the query as production and the emulator 4.11.2 do (golden
// production/cancel.json): "q":null for a listen that loads the whole node, a query without
// bounds or limit whatever its index; otherwise a list of one query object, with sin and ein
// set to true where the client left them out. JS joins that list into a query id and removes
// the listen with that id, raising permission_denied (PersistentConnection.ts:954-966); since
// its id was built from the query object as sent, a limit query without bounds is not found
// and is dropped silently, on Firebase as here. Android and iOS remove every listen at path.
func EncodeListenRevoked(path string, q *Query) []byte {
	var qs []Query
	if q != nil && (q.Limit != nil || q.HasStart() || q.HasEnd()) {
		c := *q
		yes := true
		if c.StartInclusive == nil {
			c.StartInclusive = &yes
		}
		if c.EndInclusive == nil {
			c.EndInclusive = &yes
		}
		qs = []Query{c}
	}
	return encodePush(PushListenRevoked, struct {
		Path  string  `json:"p"`
		Query []Query `json:"q"`
	}{PushPath(path), qs})
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

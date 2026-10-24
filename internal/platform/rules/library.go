package rules

import (
	"crypto/md5"
	"crypto/sha256"
	"hash"
	"hash/crc32"
	"math"
	"strings"
)

// The library's two dispatch tables: the functions of each type, and the global and namespaced
// functions (int(), float(), string(), path(), math.*, hashing.*, latlng.value, timestamp.*,
// duration.*, debug()). The interpreter reaches them through callMethod and callFunction. The
// lookups (get, exists, getAfter, existsAfter, firestore.*) are not here: lookup.go has them.
// Errors are an *evalError or an *abort, nothing else.
//
// The functions and their behaviour are the rules.* reference's
// (firebase.google.com/docs/reference/rules), corrected by production where Pyric recorded it
// differing: math.isInfinite does not exist ("Function not found error: Name:
// [math.isInfinite]."), math.ceil and math.floor return floats, hashing returns bytes whose
// hex is upper case and crc32 little-endian, and toBase64 is URL-safe and padded.

// libraryNamespaces are the namespaces of the rules.* reference whose functions callFunction
// serves. firestore (Storage's lookups) is not one of them: lookup.go has it.
var libraryNamespaces = map[string]bool{
	"duration": true, "hashing": true, "latlng": true, "math": true, "timestamp": true,
}

// libraryFunctions are the global and namespaced functions of the rules.* reference
// (firebase.google.com/docs/reference/rules/rules), by the name callFunction takes. The checker
// (S1-C) resolves calls with isLibraryFunction. math.isInfinite is in the reference but not in
// production (Pyric math-namespace, time-math-and-casts, ast-strictness: "Function not found
// error: Name: [math.isInfinite]."), so it is unknown here too; so is bool(), which is in
// neither (Pyric bool-function). debug returns its argument (callFunction).
var libraryFunctions = map[string]bool{
	"debug": true, "float": true, "int": true, "path": true, "string": true,
	"duration.abs": true, "duration.time": true, "duration.value": true,
	"hashing.crc32": true, "hashing.crc32c": true, "hashing.md5": true, "hashing.sha256": true,
	"math.abs": true, "math.ceil": true, "math.floor": true,
	"math.isNaN": true, "math.pow": true, "math.round": true, "math.sqrt": true,
	"latlng.value": true, "timestamp.date": true, "timestamp.value": true,
}

// isLibraryFunction reports whether name ("int", "math.abs") is a global or namespaced library
// function. The same in both services.
func isLibraryFunction(name string) bool { return libraryFunctions[name] }

// isLibraryNamespace reports whether name ("math") is a namespace of library functions.
func isLibraryNamespace(name string) bool { return libraryNamespaces[name] }

// callMethod calls recv.name(args). A method the receiver's type does not have is the official
// "Function not found error: Name: [name]."; a method of null is a null value error.
func callMethod(m *meter, recv Value, name string, args []Value) (Value, error) {
	if err := reserved(recv); err != nil {
		return Value{}, err
	}
	for _, a := range args {
		if err := reserved(a); err != nil {
			return Value{}, err
		}
	}
	switch recv.kind {
	case KindString:
		return stringMethod(m, recv.str(), name, args)
	case KindBytes:
		return bytesMethod(m, recv.str(), name, args)
	case KindList:
		return listMethod(m, recv.items(), name, args)
	case KindSet:
		return setMethod(m, recv.items(), name, args)
	case KindMap:
		return mapMethod(m, recv.mapv(), name, args)
	case KindMapDiff:
		return mapDiffMethod(m, recv.diff(), name, args)
	case KindTimestamp:
		return timestampMethod(m, recv.instant(), name, args)
	case KindDuration:
		return durationMethod(recv.dur(), name, args)
	case KindLatLng:
		return latLngMethod(recv.point(), name, args)
	case KindPath:
		return pathMethod(m, recv.segments(), name, args)
	case KindNull:
		return Value{}, errNull()
	}
	return Value{}, errNotFound(name)
}

// callFunction calls the global or namespaced function name ("int", "math.abs") with args.
func callFunction(m *meter, name string, args []Value) (Value, error) {
	for _, a := range args {
		if err := reserved(a); err != nil {
			return Value{}, err
		}
	}
	switch ns, _, _ := strings.Cut(name, "."); {
	case !libraryFunctions[name]:
		return Value{}, errNotFound(name)
	case ns == "math":
		return mathFunction(name, args)
	case ns == "hashing":
		return hashFunction(m, name, args)
	case ns == "duration":
		return durationFunction(name, args)
	case ns == "timestamp":
		return timestampFunction(name, args)
	case ns == "latlng":
		return latLngFunction(name, args)
	}
	switch name {
	case "debug":
		// Deployed rules return the first argument, as the emulator does and as the reference
		// says ("has no effect in production"): debug(true), debug(1) == 1, debug('s') == 's' and
		// debug({'a': 1}).a == 1 allow, and debug(1, 2) == 1 allows where == 2 and != 1 deny.
		// debug() is an error: == null and != null both deny (S7-R on chatapp-8ae21,
		// test/oracle/rules/production/deployed-firestore.json). Production's text for it is
		// not observable (Firestore answers 403 with no reason). The Rules Test API alone has no
		// debug() ("Function not found error: Name: [debug].": S0-A aruma/errors/debug_function,
		// aruma/storage/debug), an artefact of it (rulestest.Exclusion).
		if len(args) == 0 {
			return Value{}, errArity(0, []string{"debug(any)"})
		}
		return args[0], nil
	case "int":
		return toInt(args)
	case "float":
		return toFloat(args)
	case "string":
		if len(args) != 1 {
			return Value{}, mismatch("string", args, []string{"int"}, []string{"float"}, []string{"null"},
				[]string{"string"}, []string{"bool"}, []string{"path"})
		}
		s, err := toString(args[0])
		if err != nil {
			return Value{}, err
		}
		return newString(m, s)
	}
	// path
	if len(args) != 1 || args[0].kind != KindString {
		return Value{}, mismatch("path", args, []string{"string"})
	}
	return parsePath(m, args[0].str())
}

// toInt is int(x): an int as it is; a float toward zero, saturating at the ends of int64, NaN
// to 0, as Java's cast (production, Rules Test API, S0-A and Pyric: int(float('1e20')) is
// 9223372036854775807); a string as Java's Long.parseLong reads it: a sign and digits, nothing
// else, not even spaces (Pyric conversion-functions).
func toInt(args []Value) (Value, error) {
	forms := [][]string{{"int"}, {"float"}, {"string"}}
	if len(args) != 1 {
		return Value{}, mismatch("int", args, forms...)
	}
	switch a := args[0]; a.kind {
	case KindInt:
		return a, nil
	case KindFloat:
		f := a.float()
		switch {
		case math.IsNaN(f):
			return Int(0), nil
		case f >= math.MaxInt64:
			return Int(math.MaxInt64), nil
		case f <= math.MinInt64:
			return Int(math.MinInt64), nil
		}
		return Int(int64(f)), nil
	case KindString:
		i, ok := parseJavaLong(a.str())
		if !ok {
			return Value{}, errConversion(a.str(), KindString, KindInt)
		}
		return Int(i), nil
	}
	return Value{}, mismatch("int", args, forms...)
}

// toFloat is float(x): an int or a float as a float, a string as Java's Double.parseDouble
// reads it (strings.go).
func toFloat(args []Value) (Value, error) {
	forms := [][]string{{"int"}, {"float"}, {"string"}}
	if len(args) != 1 {
		return Value{}, mismatch("float", args, forms...)
	}
	switch a := args[0]; a.kind {
	case KindInt, KindFloat:
		return Float(a.number()), nil
	case KindString:
		f, ok := parseJavaDouble(a.str())
		if !ok {
			return Value{}, errConversion(a.str(), KindString, KindFloat)
		}
		return Float(f), nil
	}
	return Value{}, mismatch("float", args, forms...)
}

// mathFunction calls math.name. The results' types are production's (Pyric math-namespace,
// time-math-and-casts): abs keeps its argument's type and wraps at the smallest int, as Java's
// Math.abs; ceil, floor, sqrt and pow are floats; round is an int, rounding half up as Java's
// Math.round (round(-2.5) is -2), saturating, NaN to 0; isNaN is a bool.
func mathFunction(name string, args []Value) (Value, error) {
	switch name {
	case "math.abs":
		if len(args) != 1 || !args[0].isNumber() {
			return Value{}, mismatch(name, args, []string{"int"}, []string{"float"})
		}
		if args[0].kind == KindInt {
			i := args[0].integer()
			if i < 0 {
				i = -i // the smallest int wraps to itself
			}
			return Int(i), nil
		}
		return Float(math.Abs(args[0].float())), nil
	case "math.pow":
		if len(args) != 2 || !args[0].isNumber() || !args[1].isNumber() {
			return Value{}, mismatch(name, args, []string{"float", "float"})
		}
		return Float(math.Pow(args[0].number(), args[1].number())), nil
	}
	if len(args) != 1 || !args[0].isNumber() {
		return Value{}, mismatch(name, args, []string{"float"})
	}
	x := args[0].number()
	switch name {
	case "math.ceil":
		return Float(math.Ceil(x)), nil
	case "math.floor":
		return Float(math.Floor(x)), nil
	case "math.sqrt":
		return Float(math.Sqrt(x)), nil
	case "math.isNaN":
		return Bool(math.IsNaN(x)), nil
	}
	// math.round
	if args[0].kind == KindInt {
		return args[0], nil
	}
	r := math.Floor(x + 0.5)
	if x-math.Floor(x) < 0.5 { // x + 0.5 may round up in floating point when it should not
		r = math.Floor(x)
	}
	return toInt([]Value{Float(r)})
}

// hashFunction calls hashing.name on a string's UTF-8 bytes or on bytes, and returns the digest
// as bytes; crc32 and crc32c little-endian (Pyric bytes-toutf8-and-hashing: crc32('123456789')
// is 2639F4CB, production).
func hashFunction(m *meter, name string, args []Value) (Value, error) {
	if len(args) != 1 || args[0].kind != KindString && args[0].kind != KindBytes {
		return Value{}, mismatch(name, args, []string{"bytes"}, []string{"string"})
	}
	data := args[0].str()
	if err := m.work(int64(len(data))); err != nil {
		return Value{}, err
	}
	if args[0].kind == KindString && hasLoneSurrogate(data) {
		// A string hashes as its UTF-8, a lone surrogate as '?' (toUTF8), copied.
		if err := m.alloc(int64(len(data))); err != nil {
			return Value{}, err
		}
		data = toUTF8(data)
	}
	if err := chargeString(m, sha256.Size+hashChunk); err != nil {
		return Value{}, err
	}
	var h hash.Hash
	switch name {
	case "hashing.md5":
		h = md5.New()
	case "hashing.sha256":
		h = sha256.New()
	case "hashing.crc32":
		h = crc32.NewIEEE()
	default:
		h = crc32.New(castagnoli)
	}
	// In chunks, so a long string is not copied whole into bytes.
	buf := make([]byte, min(len(data), hashChunk))
	for i := 0; i < len(data); i += hashChunk {
		n := copy(buf, data[i:])
		h.Write(buf[:n])
	}
	sum := h.Sum(nil)
	if strings.HasPrefix(name, "hashing.crc32") {
		// crc32 sums are big-endian in Go; production gives the bytes little-endian.
		sum[0], sum[1], sum[2], sum[3] = sum[3], sum[2], sum[1], sum[0]
	}
	return Value{kind: KindBytes, x: string(sum)}, nil
}

// hashChunk is the size of the pieces hashFunction feeds a hash.
const hashChunk = 4096

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// earthRadius is the mean radius of the Earth in meters that distance() uses (hidane, measured
// on the emulator).
const earthRadius = 6_371_009.0

// latLngFunction calls latlng.value(lat, lng): degrees within [-90, 90] and [-180, 180].
func latLngFunction(name string, args []Value) (Value, error) {
	if len(args) != 2 || !args[0].isNumber() || !args[1].isNumber() {
		return Value{}, mismatch(name, args, []string{"float", "float"})
	}
	lat, lng := args[0].number(), args[1].number()
	if !(lat >= -90 && lat <= 90) || !(lng >= -180 && lng <= 180) {
		// Pyric duration-and-latlng: "… latitude: -100.0, longitude: -122.08."
		return Value{}, errorf("Latitude should be in [-90,90] range and longitude should be in [-180,180] range. latitude: %s, longitude: %s.",
			javaDouble(lat), javaDouble(lng))
	}
	return LatLng(lat, lng), nil
}

// latLngMethod calls p.name(args): latitude(), longitude(), and distance(other) in meters
// along a great circle (haversine).
func latLngMethod(p latLng, name string, args []Value) (Value, error) {
	switch name {
	case "latitude", "longitude":
		if len(args) != 0 {
			return Value{}, mismatch("latlng."+name, args, []string{})
		}
		if name == "latitude" {
			return Float(p.lat), nil
		}
		return Float(p.lng), nil
	case "distance":
		if len(args) != 1 || args[0].kind != KindLatLng {
			return Value{}, mismatch("latlng.distance", args, []string{"latlng"})
		}
		q := args[0].point()
		rad := math.Pi / 180
		dLat, dLng := (q.lat-p.lat)*rad, (q.lng-p.lng)*rad
		h := math.Pow(math.Sin(dLat/2), 2) + math.Cos(p.lat*rad)*math.Cos(q.lat*rad)*math.Pow(math.Sin(dLng/2), 2)
		return Float(2 * earthRadius * math.Asin(math.Sqrt(h))), nil
	}
	return Value{}, errNotFound(name)
}

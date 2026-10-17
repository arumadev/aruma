// Package schema embeds the JSON Schemas of the generated JSON configuration files. Tests
// validate every generated file against them, and they document the formats for clients.
package schema

import "embed"

// FS holds *.schema.json.
//
//go:embed *.schema.json
var FS embed.FS

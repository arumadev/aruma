package rules

// LiteralRegexBytes returns the estimated programs of a ruleset's literal patterns, as
// regex_bytes counts them, for the external tests.
func LiteralRegexBytes(rs *Ruleset) int64 { return rs.regexes.literalBytes() }

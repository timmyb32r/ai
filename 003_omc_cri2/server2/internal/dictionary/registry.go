package dictionary

// DictInfo describes a dictionary available to the system.
// Mirrors the ASR ModelInfo pattern in asr/registry.go.
type DictInfo struct {
	// Codename is the short name used in env var DICT (e.g. "bkrs", "cedict").
	Codename string
	// Description is a human-readable label.
	Description string
	// RequiredFile is the file that must exist in the container image
	// for this dictionary to be usable. Validated by docker-entrypoint.sh.
	RequiredFile string
}

// DictRegistry maps short codenames to DictInfo entries.
// Add new dictionaries here with a stable codename.
var DictRegistry = map[string]DictInfo{
	"bkrs": {
		Codename:     "bkrs",
		Description:  "BKRS (大БКРС) — Chinese-Russian dictionary from bkrs.info",
		RequiredFile: "dabkrs.gz",
	},
	"cedict": {
		Codename:     "cedict",
		Description:  "CC-CEDICT — Chinese-English dictionary (community maintained)",
		RequiredFile: "cedict_ts.u8",
	},
	"wiktionary": {
		Codename:     "wiktionary",
		Description:  "kaikki.org Wiktionary — Chinese-English from pre-parsed JSONL dump",
		RequiredFile: "zh-extract.jsonl.gz",
	},
}

// LookupDict returns the DictInfo for a codename.
// The second return value is false if the codename is not registered.
func LookupDict(codename string) (DictInfo, bool) {
	info, ok := DictRegistry[codename]
	return info, ok
}

package pinyin

import "testing"

func TestValidateHieroglyphPinyin_Valid(t *testing.T) {
	valid := []string{
		// bare / tone digits / diacritics all normalise to the same syllable
		"ma", "ma1", "ma3", "mǎ", "mā",
		// special apical syllables (zhi/chi/shi/ri/zi/ci/si)
		"zhi", "chi", "shi", "ri", "zi", "ci", "si",
		// zero-initial
		"a", "e", "o", "ai", "ao", "ou", "an", "en", "ang", "eng", "er",
		// y / w onset
		"yi", "ya", "ye", "yao", "you", "yan", "yin", "ying", "yong",
		"yu", "yue", "yuan", "yun", "wu", "wa", "wo", "wei", "wang", "weng",
		// ü finals (with ü, v, and u: spellings)
		"lü", "lv", "lu:", "nü", "lüe", "nüe",
		// j/q/x + ü written as u
		"ju", "qu", "xu", "jue", "quan", "xun",
		// reported-bug syllables
		"hē", "hù", "tā", "men", "tǔ", "lā",
		// erhua + interjections
		"r", "m", "n", "ng", "hm", "hng", "ê",
		// a spread of ordinary syllables
		"zhuang", "shuang", "guang", "xiong", "jiong", "hui", "duo", "lüe",
	}
	for _, s := range valid {
		if err := ValidateHieroglyphPinyin(s); err != nil {
			t.Errorf("ValidateHieroglyphPinyin(%q) = %v, want valid", s, err)
		}
	}
}

func TestValidateHieroglyphPinyin_ImpossibleCombos(t *testing.T) {
	// Parts exist in isolation but the initial+final pair is illegal.
	invalid := []string{
		"fiang", // f cannot take -iang
		"riang", // r cannot take -iang
		"ciang", // c cannot take -iang
		"biang", // not a standard syllable
		"zi a",  // space
		"fe",    // f + e is not a syllable
		"ju e",  // embedded space
	}
	for _, s := range invalid {
		if err := ValidateHieroglyphPinyin(s); err == nil {
			t.Errorf("ValidateHieroglyphPinyin(%q) = nil, want error", s)
		}
	}
}

func TestValidateHieroglyphPinyin_JunkRejected(t *testing.T) {
	// Exactly the failure modes the dictionary bug produced.
	invalid := []string{
		"",       // empty
		"?",      // sentinel / unknown
		"hehu",   // two merged syllables
		"tamen",  // two merged syllables
		"hēhù",   // merged, with diacritics
		"tā men", // contains a space
		"tamen,", // trailing punctuation
		"men;",   // trailing punctuation
		"земля",  // russian gloss leaked in
		"[m1]",   // markup leaked in
		"ma5b",   // digit not at the end / trailing junk
		"3ma",    // leading digit
	}
	for _, s := range invalid {
		if err := ValidateHieroglyphPinyin(s); err == nil {
			t.Errorf("ValidateHieroglyphPinyin(%q) = nil, want error", s)
		}
	}
}

func TestNumberedToDiacritic(t *testing.T) {
	cases := map[string]string{
		"tian1": "tiān",
		"Wen4":  "wèn", // capitalised (CEDICT proper noun)
		"wen4":  "wèn",
		"de5":   "de", // neutral tone → digit dropped
		"de":    "de", // no digit
		"ma3":   "mǎ",
		"lu:3":  "lǚ", // u: → ü
		"lv3":   "lǚ", // v → ü
		"hao3":  "hǎo",
		"gou3":  "gǒu", // ou → mark o
		"jiu4":  "jiù", // no a/e, last vowel u
		"shi4":  "shì",
		"er2":   "ér",
	}
	for in, want := range cases {
		if got := NumberedToDiacritic(in); got != want {
			t.Errorf("NumberedToDiacritic(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateHieroglyphPinyin_TableCoverage(t *testing.T) {
	// Every canonical syllable must validate (guards against typos in the list
	// and against splitInitial/compat drifting apart).
	for _, s := range canonicalSyllables {
		if err := ValidateHieroglyphPinyin(s); err != nil {
			t.Errorf("canonical syllable %q rejected: %v", s, err)
		}
	}
}

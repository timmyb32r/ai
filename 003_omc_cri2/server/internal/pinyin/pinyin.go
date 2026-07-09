// Package pinyin validates a single Hanyu Pinyin syllable (the pronunciation of
// one Chinese character) using the official structural model of Pinyin:
//
//	syllable = [initial] + final
//
// where the initial is an optional leading consonant and the final is the
// mandatory remainder (medial + nucleus + optional coda).
//
// Validation is table-driven, NOT based on phonetic heuristics such as
// "consonant + vowels + consonant". A syllable is accepted only when:
//
//  1. a valid initial is stripped (or there is none), AND
//  2. the remaining final is a known final, AND
//  3. the (initial, final) pair is present in the compatibility table.
//
// The compatibility table is derived once, at package initialisation, from a
// canonical list of every valid toneless syllable in standard orthography.
// This guarantees the table is internally consistent and forbids impossible
// combinations such as "fiang", "riang" or "ciang" even though their parts
// exist in isolation.
package pinyin

import (
	"fmt"
	"strings"
)

// twoLetterInitials must be tried before single-letter initials so that e.g.
// "sh" wins over "s" for "shuang".
var twoLetterInitials = []string{"zh", "ch", "sh"}

// oneLetterInitials — every single-letter initial, including the semivowels
// y and w which begin zero-consonant syllables in standard orthography.
var oneLetterInitials = []string{
	"b", "p", "m", "f",
	"d", "t", "n", "l",
	"g", "k", "h",
	"j", "q", "x",
	"r",
	"z", "c", "s",
	"y", "w",
}

// standaloneSyllables are complete syllables that carry no initial+final
// structure: bare interjections and the erhua suffix. They bypass
// decomposition entirely.
var standaloneSyllables = map[string]bool{
	"m": true, "n": true, "ng": true, // 呒 呣 嗯 唔
	"hm": true, "hng": true, // 噷 哼
	"r": true, // erhua suffix (儿 in 儿化)
	"ê": true, // 欸
}

// canonicalSyllables is the authoritative list of valid toneless Pinyin
// syllables in standard orthography. finalsSet and the compat table are built
// from it. Grouped by (spelled) initial for auditability.
var canonicalSyllables = []string{
	// zero initial (pure vowel onset)
	"a", "o", "e", "ai", "ei", "ao", "ou", "an", "en", "ang", "eng", "er",
	// y-
	"ya", "yo", "ye", "yao", "you", "yan", "yang", "yin", "ying", "yong", "yi",
	"yu", "yue", "yuan", "yun",
	// w-
	"wa", "wo", "wai", "wei", "wan", "wang", "wen", "weng", "wu",
	// b-
	"ba", "bo", "bai", "bei", "bao", "ban", "ben", "bang", "beng",
	"bi", "bie", "biao", "bian", "bin", "bing", "bu",
	// p-
	"pa", "po", "pai", "pei", "pao", "pou", "pan", "pen", "pang", "peng",
	"pi", "pie", "piao", "pian", "pin", "ping", "pu",
	// m-
	"ma", "mo", "me", "mai", "mei", "mao", "mou", "man", "men", "mang", "meng",
	"mi", "mie", "miao", "miu", "mian", "min", "ming", "mu",
	// f-
	"fa", "fo", "fei", "fou", "fan", "fen", "fang", "feng", "fu",
	// d-
	"da", "de", "dai", "dei", "dao", "dou", "dan", "den", "dang", "deng", "dong",
	"di", "dia", "die", "diao", "diu", "dian", "ding",
	"du", "duo", "dui", "duan", "dun",
	// t-
	"ta", "te", "tai", "tei", "tao", "tou", "tan", "tang", "teng", "tong",
	"ti", "tie", "tiao", "tian", "ting",
	"tu", "tuo", "tui", "tuan", "tun",
	// n-
	"na", "ne", "nai", "nei", "nao", "nou", "nan", "nen", "nang", "neng", "nong",
	"ni", "nie", "niao", "niu", "nian", "nin", "niang", "ning",
	"nu", "nuo", "nuan", "nü", "nüe",
	// l-
	"la", "lo", "le", "lai", "lei", "lao", "lou", "lan", "lang", "leng", "long",
	"li", "lia", "lie", "liao", "liu", "lian", "lin", "liang", "ling",
	"lu", "luo", "luan", "lun", "lü", "lüe",
	// g-
	"ga", "ge", "gai", "gei", "gao", "gou", "gan", "gen", "gang", "geng", "gong",
	"gu", "gua", "guo", "guai", "gui", "guan", "gun", "guang",
	// k-
	"ka", "ke", "kai", "kei", "kao", "kou", "kan", "ken", "kang", "keng", "kong",
	"ku", "kua", "kuo", "kuai", "kui", "kuan", "kun", "kuang",
	// h-
	"ha", "he", "hai", "hei", "hao", "hou", "han", "hen", "hang", "heng", "hong",
	"hu", "hua", "huo", "huai", "hui", "huan", "hun", "huang",
	// j-
	"ji", "jia", "jie", "jiao", "jiu", "jian", "jin", "jiang", "jing", "jiong",
	"ju", "jue", "juan", "jun",
	// q-
	"qi", "qia", "qie", "qiao", "qiu", "qian", "qin", "qiang", "qing", "qiong",
	"qu", "que", "quan", "qun",
	// x-
	"xi", "xia", "xie", "xiao", "xiu", "xian", "xin", "xiang", "xing", "xiong",
	"xu", "xue", "xuan", "xun",
	// zh-
	"zha", "zhe", "zhi", "zhai", "zhei", "zhao", "zhou", "zhan", "zhen",
	"zhang", "zheng", "zhong",
	"zhu", "zhua", "zhuo", "zhuai", "zhui", "zhuan", "zhun", "zhuang",
	// ch-
	"cha", "che", "chi", "chai", "chao", "chou", "chan", "chen",
	"chang", "cheng", "chong",
	"chu", "chua", "chuo", "chuai", "chui", "chuan", "chun", "chuang",
	// sh-
	"sha", "she", "shi", "shai", "shei", "shao", "shou", "shan", "shen",
	"shang", "sheng",
	"shu", "shua", "shuo", "shuai", "shui", "shuan", "shun", "shuang",
	// r-
	"re", "ri", "rao", "rou", "ran", "ren", "rang", "reng", "rong",
	"ru", "rua", "ruo", "rui", "ruan", "run",
	// z-
	"za", "ze", "zi", "zai", "zei", "zao", "zou", "zan", "zen", "zang", "zeng", "zong",
	"zu", "zuo", "zui", "zuan", "zun",
	// c-
	"ca", "ce", "ci", "cai", "cao", "cou", "can", "cen", "cang", "ceng", "cong",
	"cu", "cuo", "cui", "cuan", "cun",
	// s-
	"sa", "se", "si", "sai", "sao", "sou", "san", "sen", "sang", "seng", "song",
	"su", "suo", "sui", "suan", "sun",
}

var (
	// finalsSet is the set of all valid finals (Step 3). Derived from
	// canonicalSyllables so it stays in sync with the compat table.
	finalsSet = map[string]bool{}
	// compat[initial][final] reports whether that combination is valid
	// (Step 4). initial is "" for zero-initial syllables.
	compat = map[string]map[string]bool{}
	// initialsSet is the set of recognised initials, for error reporting.
	initialsSet = map[string]bool{}
)

func init() {
	for _, in := range twoLetterInitials {
		initialsSet[in] = true
	}
	for _, in := range oneLetterInitials {
		initialsSet[in] = true
	}
	for _, syl := range canonicalSyllables {
		initial, final := splitInitial(syl)
		finalsSet[final] = true
		if compat[initial] == nil {
			compat[initial] = map[string]bool{}
		}
		compat[initial][final] = true
	}
}

// splitInitial strips the longest matching initial from an already-normalised
// syllable and returns (initial, final). If no initial applies — or stripping
// it would leave an empty final — the whole syllable is treated as the final
// with an empty initial.
func splitInitial(s string) (initial, final string) {
	for _, in := range twoLetterInitials {
		if strings.HasPrefix(s, in) && len(s) > len(in) {
			return in, s[len(in):]
		}
	}
	for _, in := range oneLetterInitials {
		if strings.HasPrefix(s, in) && len(s) > len(in) {
			return in, s[len(in):]
		}
	}
	return "", s
}

// ValidateHierogliphPinyin reports whether s is a single valid Pinyin syllable
// (the reading of one Chinese character). It returns nil when valid, or a
// descriptive error explaining which structural rule failed.
//
// Tone marks (both digits like "ma3" and diacritics like "mǎ") are tolerated
// and stripped during normalisation; the letter ü may be written as "ü", "v"
// or "u:".
func ValidateHierogliphPinyin(s string) error {
	norm, err := normalize(s)
	if err != nil {
		return err
	}

	if standaloneSyllables[norm] {
		return nil
	}

	initial, final := splitInitial(norm)

	if !finalsSet[final] {
		return fmt.Errorf("pinyin %q: %q is not a valid final", s, final)
	}
	if !compat[initial][final] {
		if initial == "" {
			return fmt.Errorf("pinyin %q: final %q cannot stand without an initial", s, final)
		}
		return fmt.Errorf("pinyin %q: initial %q is incompatible with final %q", s, initial, final)
	}
	return nil
}

// IsValidHierogliphPinyin is the boolean form of ValidateHierogliphPinyin.
func IsValidHierogliphPinyin(s string) bool {
	return ValidateHierogliphPinyin(s) == nil
}

// toneMarks maps a base vowel to its tone-marked forms for tones 1..4.
var toneMarks = map[rune][4]rune{
	'a': {'ā', 'á', 'ǎ', 'à'},
	'e': {'ē', 'é', 'ě', 'è'},
	'i': {'ī', 'í', 'ǐ', 'ì'},
	'o': {'ō', 'ó', 'ǒ', 'ò'},
	'u': {'ū', 'ú', 'ǔ', 'ù'},
	'ü': {'ǖ', 'ǘ', 'ǚ', 'ǜ'},
}

func isPlainVowel(r rune) bool {
	return r == 'a' || r == 'e' || r == 'i' || r == 'o' || r == 'u' || r == 'ü'
}

// NumberedToDiacritic converts a numbered pinyin syllable (e.g. "tian1",
// "Wen4", "lu:3", "lv3") to its tone-mark form ("tiān", "wèn", "lǚ"). Neutral
// tone (5, 0, or no digit) yields the plain syllable. Input is lower-cased and
// v / u: are treated as ü. Non-pinyin input is returned lower-cased unchanged.
func NumberedToDiacritic(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "u:", "ü")
	s = strings.ReplaceAll(s, "v", "ü")
	if s == "" {
		return s
	}
	r := []rune(s)
	tone := 0
	if last := r[len(r)-1]; last >= '1' && last <= '5' {
		tone = int(last - '0')
		r = r[:len(r)-1]
	}
	if tone == 0 || tone == 5 {
		return string(r)
	}
	idx := toneVowelIndex(r)
	if idx < 0 {
		return string(r)
	}
	marks, ok := toneMarks[r[idx]]
	if !ok {
		return string(r)
	}
	r[idx] = marks[tone-1]
	return string(r)
}

// toneVowelIndex picks which vowel carries the tone mark, per standard rules:
// 'a' or 'e' if present; otherwise the 'o' in "ou"; otherwise the last vowel.
func toneVowelIndex(r []rune) int {
	for i, c := range r {
		if c == 'a' {
			return i
		}
	}
	for i, c := range r {
		if c == 'e' {
			return i
		}
	}
	for i := 0; i+1 < len(r); i++ {
		if r[i] == 'o' && r[i+1] == 'u' {
			return i
		}
	}
	for i := len(r) - 1; i >= 0; i-- {
		if isPlainVowel(r[i]) {
			return i
		}
	}
	return -1
}

// SegmentByCount splits an un-spaced Pinyin string into exactly n valid
// syllables and returns them with their original spelling and tone marks
// preserved. It is the structural counterpart to per-character alignment:
// since each Chinese character is one syllable, a word's run-together pinyin
// (e.g. "yánchǐ") is split into one syllable per character ("yán", "chǐ").
//
// The splitter uses maximal munch (longest valid syllable first) with
// backtracking, constrained to produce exactly n pieces — which resolves the
// classic pinyin boundary ambiguities in the overwhelming majority of cases,
// especially since tone marks already pin most boundaries. Apostrophes and
// dashes are treated as explicit syllable separators. ok is false when no
// segmentation into n valid syllables exists.
func SegmentByCount(s string, n int) ([]string, bool) {
	if n <= 0 {
		return nil, false
	}
	// Apostrophes/dashes are hard syllable boundaries in Pinyin orthography.
	s = strings.Map(func(r rune) rune {
		if r == '\'' || r == '’' || r == '‘' || r == 'ʼ' || r == '-' {
			return ' '
		}
		return r
	}, s)

	// Segment each whitespace-separated group independently, then require the
	// concatenation to hit exactly n syllables.
	groups := strings.Fields(s)
	if len(groups) == 0 {
		return nil, false
	}
	if len(groups) > n {
		return nil, false
	}

	// Distribute the n target syllables across groups via backtracking: try
	// giving each group as many syllables as it can take.
	var result []string
	var assign func(gi, remaining int) bool
	assign = func(gi, remaining int) bool {
		if gi == len(groups) {
			return remaining == 0
		}
		groupsLeft := len(groups) - gi - 1
		// This group must leave at least one syllable for every later group.
		maxHere := remaining - groupsLeft
		for k := 1; k <= maxHere; k++ {
			if seg, ok := segmentExact(groups[gi], k); ok {
				result = append(result, seg...)
				if assign(gi+1, remaining-k) {
					return true
				}
				result = result[:len(result)-len(seg)]
			}
		}
		return false
	}
	if assign(0, n) {
		out := make([]string, len(result))
		copy(out, result)
		return out, true
	}
	return nil, false
}

// segmentExact splits a single group (no separators) into exactly n valid
// syllables using maximal munch with backtracking.
func segmentExact(group string, n int) ([]string, bool) {
	runes := []rune(group)
	var res []string
	var rec func(pos, left int) bool
	rec = func(pos, left int) bool {
		if left == 0 {
			return pos == len(runes)
		}
		if pos >= len(runes) {
			return false
		}
		// Leave at least one rune for each of the remaining syllables.
		maxEnd := len(runes) - (left - 1)
		for end := maxEnd; end > pos; end-- {
			cand := string(runes[pos:end])
			if IsValidHierogliphPinyin(cand) {
				res = append(res, cand)
				if rec(end, left-1) {
					return true
				}
				res = res[:len(res)-1]
			}
		}
		return false
	}
	if rec(0, n) {
		out := make([]string, len(res))
		copy(out, res)
		return out, true
	}
	return nil, false
}

// normalize performs Step 1: lowercase, drop the tone (digit or diacritic),
// and canonicalise the ü spelling. It rejects strings that still contain
// characters that cannot belong to a bare Pinyin syllable (punctuation,
// whitespace, non-latin letters, leftover digits, …).
func normalize(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("pinyin: empty syllable")
	}
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "u:", "ü")

	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case r >= 'a' && r <= 'z':
			if r == 'v' { // common ASCII substitute for ü
				b.WriteRune('ü')
			} else {
				b.WriteRune(r)
			}
		case r == 'ü':
			b.WriteRune('ü')
		case isToneVowel(r):
			b.WriteRune(stripTone(r))
		case r >= '1' && r <= '5' && i == len(runes)-1:
			// trailing tone digit — drop it
		default:
			return "", fmt.Errorf("pinyin %q: contains illegal character %q", s, string(r))
		}
	}
	out := b.String()
	if out == "" {
		return "", fmt.Errorf("pinyin %q: nothing left after normalisation", s)
	}
	return out, nil
}

// isToneVowel reports whether r is a tone-marked pinyin vowel.
func isToneVowel(r rune) bool {
	switch r {
	case 'ā', 'á', 'ǎ', 'à',
		'ē', 'é', 'ě', 'è',
		'ī', 'í', 'ǐ', 'ì',
		'ō', 'ó', 'ǒ', 'ò',
		'ū', 'ú', 'ǔ', 'ù',
		'ǖ', 'ǘ', 'ǚ', 'ǜ',
		'ń', 'ň', 'ǹ', // tone-marked syllabic n (嗯)
		'ê', 'ế', 'ề': // ê variants (欸)
		return true
	}
	return false
}

// stripTone maps a tone-marked vowel to its base letter, preserving the ü
// umlaut and the ê / n base forms.
func stripTone(r rune) rune {
	switch r {
	case 'ā', 'á', 'ǎ', 'à':
		return 'a'
	case 'ē', 'é', 'ě', 'è':
		return 'e'
	case 'ī', 'í', 'ǐ', 'ì':
		return 'i'
	case 'ō', 'ó', 'ǒ', 'ò':
		return 'o'
	case 'ū', 'ú', 'ǔ', 'ù':
		return 'u'
	case 'ǖ', 'ǘ', 'ǚ', 'ǜ':
		return 'ü'
	case 'ń', 'ň', 'ǹ':
		return 'n'
	case 'ê', 'ế', 'ề':
		return 'ê'
	}
	return r
}

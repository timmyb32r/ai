package dictionary

import (
	"os"
	"runtime"
	"sync"
	"testing"

	"github.com/criradio/server/internal/pinyin"
)

// bkrsDumpPath returns a path to a BKRS dump to validate against, or "" if none
// is available (in which case the corpus-wide test is skipped). Override with
// the BKRS_PATH environment variable.
func bkrsDumpPath() string {
	candidates := []string{
		os.Getenv("BKRS_PATH"),
		"/opt/dabkrs.gz",
		"/tmp/dabkrs_corpus.gz",
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// TestPerCharacterPinyin_AllValidAcrossBKRS extracts every Chinese headword
// from the BKRS dictionary, runs it through the production per-character pinyin
// generator (Lookup → CharPinyins), and asserts that every produced syllable is
// a structurally valid single Pinyin syllable containing nothing extraneous —
// no merged syllables, no leftover punctuation, no leaked gloss/markup. The
// sweep is parallelised across CPUs. "?" and "" are the explicit
// unknown/absent markers and are not validated.
func TestPerCharacterPinyin_AllValidAcrossBKRS(t *testing.T) {
	path := bkrsDumpPath()
	if path == "" {
		t.Skip("no BKRS dump available; set BKRS_PATH to run this test")
	}

	dict, err := LoadBKRS(path)
	if err != nil {
		t.Fatalf("load BKRS dump %q: %v", path, err)
	}
	bd := dict.(*bkrsDict)

	// Snapshot the headwords so goroutines don't race on the map.
	words := make([]string, 0, len(bd.entries))
	for w := range bd.entries {
		words = append(words, w)
	}

	type failure struct {
		word, pinyin, syllable, reason string
	}

	workers := runtime.NumCPU()
	chunk := (len(words) + workers - 1) / workers
	var (
		mu       sync.Mutex
		failures []failure
		checked  int
		wg       sync.WaitGroup
	)

	for w := 0; w < workers; w++ {
		lo := w * chunk
		if lo >= len(words) {
			break
		}
		hi := lo + chunk
		if hi > len(words) {
			hi = len(words)
		}
		wg.Add(1)
		go func(words []string) {
			defer wg.Done()
			var (
				localFail    []failure
				localChecked int
			)
			for _, word := range words {
				entry, err := dict.Lookup(word)
				if err != nil {
					continue
				}
				for _, syl := range entry.CharPinyins {
					if syl == "" || syl == "?" {
						continue // explicit absent / unknown markers
					}
					localChecked++
					if verr := pinyin.ValidateHierogliphPinyin(syl); verr != nil {
						localFail = append(localFail, failure{
							word: word, pinyin: entry.Pinyin,
							syllable: syl, reason: verr.Error(),
						})
					}
				}
			}
			mu.Lock()
			failures = append(failures, localFail...)
			checked += localChecked
			mu.Unlock()
		}(words[lo:hi])
	}
	wg.Wait()

	t.Logf("validated %d per-character syllables from %d headwords in %q",
		checked, len(words), path)

	if len(failures) > 0 {
		const maxShow = 40
		for i, f := range failures {
			if i >= maxShow {
				t.Errorf("... and %d more invalid syllables", len(failures)-maxShow)
				break
			}
			t.Errorf("word %q (pinyin %q): invalid per-character syllable %q — %s",
				f.word, f.pinyin, f.syllable, f.reason)
		}
		t.Fatalf("%d of %d per-character pinyin syllables are invalid", len(failures), checked)
	}
}

// TestPerCharacterPinyin_TrickyCases exercises the per-character splitter on the
// real BKRS orthographies that used to leak the whole-word pinyin onto a single
// character. It uses a small in-memory dump so it always runs (no external
// dependency), asserting both the exact split and full validity.
func TestPerCharacterPinyin_TrickyCases(t *testing.T) {
	dump := "" +
		// single-char entries feeding the char map
		"他\n tā\n[m1]он[/m]\n\n" +
		"们\n mén, men\n[m1]суффикс[/m]\n\n" +
		"呵\n hē, ā, kē\n[m1]дуть[/m]\n\n" +
		"护\n hù\n[m1]защищать[/m]\n\n" +
		"宣\n xuān\n[m1]объявлять[/m]\n\n" +
		"露\n lù, lòu\n[m1]роса[/m]\n\n" +
		"美\n měi\n[m1]красивый[/m]\n\n" +
		"国\n guó\n[m1]страна[/m]\n\n" +
		"政\n zhèng\n[m1]политика[/m]\n\n" +
		"府\n fǔ\n[m1]управа[/m]\n\n" +
		// multi-char entries with the problematic orthographies
		"他们\n tāmen\n[m1]они[/m]\n\n" + // run-together diacritic
		"呵护\n hēhù\n[m1]оберегать[/m]\n\n" + // run-together diacritic, multi-reading char
		"盐豉\n yánchǐ\n[m1]приправа[/m]\n\n" + // both chars absent from char map
		"宣露\n xuānlù(lòu)\n[m1]разглашать[/m]\n\n" + // parenthesised alt reading
		"空地\n kòngdì, kōngdì\n[m1]пустырь[/m]\n\n" + // comma-separated whole-word readings
		"桯凳\n tīngdèng chéngchéng\n[m1]скамья[/m]\n\n" + // space-separated whole-word readings
		"美国政府\n meiguo zhengfu\n[m1]правительство США[/m]\n\n" // sub-word grouping

	tmpFile := t.TempDir() + "/tricky.dump"
	if err := os.WriteFile(tmpFile, []byte(dump), 0644); err != nil {
		t.Fatal(err)
	}
	dict, err := LoadBKRS(tmpFile)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		word string
		want []string
	}{
		{"他们", []string{"tā", "men"}},                   // run-together diacritic, tone preserved
		{"呵护", []string{"hē", "hù"}},                    // multi-reading char no longer leaks
		{"盐豉", []string{"yán", "chǐ"}},                  // chars absent from map → structural split
		{"宣露", []string{"xuān", "lù"}},                  // parenthesised alt reading stripped
		{"空地", []string{"kòng", "dì"}},                  // comma-separated readings → first, per-char
		{"桯凳", []string{"tīng", "dèng"}},                // space-separated readings → first, per-char
		{"美国政府", []string{"měi", "guó", "zhèng", "fǔ"}}, // toneless source → char-map recovers tones
	}
	for _, tc := range cases {
		entry, err := dict.Lookup(tc.word)
		if err != nil {
			t.Errorf("%s: lookup failed: %v", tc.word, err)
			continue
		}
		if len(entry.CharPinyins) != len(tc.want) {
			t.Errorf("%s: got %v, want %v", tc.word, entry.CharPinyins, tc.want)
			continue
		}
		for i, w := range tc.want {
			got := entry.CharPinyins[i]
			if got != w {
				t.Errorf("%s[%d]: got %q, want %q (full: %v)", tc.word, i, got, w, entry.CharPinyins)
			}
			// No syllable may be the whole-word pinyin or otherwise invalid.
			if err := pinyin.ValidateHierogliphPinyin(got); err != nil {
				t.Errorf("%s[%d]: %q is not a valid syllable: %v", tc.word, i, got, err)
			}
		}
	}
}

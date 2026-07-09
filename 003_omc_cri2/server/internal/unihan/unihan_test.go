package unihan

import (
	"math"
	"os"
	"testing"
)

// realistic Unihan_Readings.txt fixture (tab-separated), verbatim shapes from
// the Unicode UCD.
const fixture = "# comment line\n" +
	"U+4E86\tkHanyuPinlu\tle(30101) liǎo(654) liào(19)\n" +
	"U+4E86\tkMandarin\tle\n" +
	"U+5730\tkHanyuPinlu\tde(7394) dì(4976)\n" +
	"U+5730\tkMandarin\tde dì\n" +
	"U+7684\tkHanyuPinlu\tde(75596) dì(157) dí(84)\n" +
	"U+7684\tkMandarin\tde\n" +
	"U+7740\tkHanyuPinlu\tzhe(10643) zháo(545) zhuó(125) zhāo(15)\n" +
	"U+7740\tkMandarin\tzhe\n" +
	// kMandarin-only character (no frequency data)
	"U+9F98\tkMandarin\tlài\n" +
	// unrelated field that must be ignored
	"U+7684\tkDefinition\tpossessive particle\n"

func loadFixture(t *testing.T) *Resolver {
	t.Helper()
	f := t.TempDir() + "/Unihan_Readings.txt"
	if err := os.WriteFile(f, []byte(fixture), 0644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(f)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResolver_TopReadingAndShare(t *testing.T) {
	r := loadFixture(t)
	cases := []struct {
		ch        rune
		wantPy    string
		wantShare float64
		source    string
	}{
		{'的', "de", 75596.0 / (75596 + 157 + 84), "kHanyuPinlu"},
		{'了', "le", 30101.0 / (30101 + 654 + 19), "kHanyuPinlu"},
		{'着', "zhe", 10643.0 / (10643 + 545 + 125 + 15), "kHanyuPinlu"},
		{'地', "de", 7394.0 / (7394 + 4976), "kHanyuPinlu"},
		{'龘', "lài", 0, "kMandarin"}, // kMandarin fallback
	}
	for _, tc := range cases {
		got, ok := r.Lookup(tc.ch)
		if !ok {
			t.Errorf("%c: not found", tc.ch)
			continue
		}
		if got.Pinyin != tc.wantPy {
			t.Errorf("%c: pinyin=%q, want %q", tc.ch, got.Pinyin, tc.wantPy)
		}
		if got.Source != tc.source {
			t.Errorf("%c: source=%q, want %q", tc.ch, got.Source, tc.source)
		}
		if math.Abs(got.Share-tc.wantShare) > 1e-9 {
			t.Errorf("%c: share=%.4f, want %.4f", tc.ch, got.Share, tc.wantShare)
		}
	}
}

func TestResolver_UnknownChar(t *testing.T) {
	r := loadFixture(t)
	if _, ok := r.Lookup('銀'); ok {
		t.Error("expected 銀 to be unknown")
	}
}

func TestResolver_NilSafe(t *testing.T) {
	var r *Resolver
	if _, ok := r.Lookup('的'); ok {
		t.Error("nil resolver must report not found")
	}
	if r.Size() != 0 {
		t.Error("nil resolver size must be 0")
	}
}

func TestResolver_ShareMeetsExpectations(t *testing.T) {
	r := loadFixture(t)
	// 的 dominant (>0.99), 地 genuinely split (~0.6).
	de, _ := r.Lookup('的')
	if de.Share < 0.99 {
		t.Errorf("的 share=%.4f, want >0.99", de.Share)
	}
	di, _ := r.Lookup('地')
	if di.Share > 0.7 || di.Share < 0.5 {
		t.Errorf("地 share=%.4f, want ~0.6 (ambiguous)", di.Share)
	}
}

package pipeline

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/criradio/server/internal/dictionary"
	"github.com/criradio/server/internal/unihan"
)

// Opt-in validation uses the deployment's real dictionaries, without network or
// model downloads. It detects absent assets and reports their resident heap.
func TestDeploymentAssets(t *testing.T) {
	dir := os.Getenv("CRI_TEST_ASSETS")
	if dir == "" {
		t.Skip("set CRI_TEST_ASSETS to validate deployment dictionaries")
	}
	a, e := dictionary.LoadBKRS(filepath.Join(dir, "dabkrs.gz"))
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := dictionary.Load(filepath.Join(dir, "cedict_ts.u8"))
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	c, e := dictionary.LoadWiktionary(filepath.Join(dir, "zh-extract.jsonl.gz"))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	u, e := unihan.Load(filepath.Join(dir, "Unihan_Readings.txt"))
	if e != nil {
		t.Fatal(e)
	}
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	t.Logf("real assets: live heap %.1f MiB, runtime allocated %.1f MiB, Unihan %d", float64(m.HeapAlloc)/(1<<20), float64(m.Sys)/(1<<20), u.Size())
	for _, d := range []dictionary.Dictionary{a, b, c} {
		if _, e := d.Lookup("中国"); e != nil {
			t.Fatal(e)
		}
	}
	runtime.KeepAlive(u)
}

package metrics

import "testing"

func TestFormatLineMatchesRustStatsProtocol(t *testing.T) {
	const mib = uint64(1024 * 1024)
	d := snapshot{
		messages: 100, compressed: 5 * mib, decompressed: 10 * mib,
		downloadBusy: 500_000_000, decompressBusy: 250_000_000,
		parsedRows: 200, parseArrow: 20 * mib, invalid: 1, parseMessages: 100, parseBusy: 750_000_000,
		insertedRows: 201, sinkArrow: 21 * mib, sinkFlushes: 2, sinkMessages: 100, sinkBusy: 900_000_000,
	}
	got := formatLine(0, d, 1_000_000_000, 250, 1536*mib)
	want := "[stats p=0] yds: 100 msg/s | comp 5.0 MiB/s | decomp 10.0 MiB/s | dl 50% busy | decomp 25% busy" +
		" || parse: 200 rows/s | 20.0 MiB/s arrow | 1 dlq/s | ~100 msg/s | 75% busy" +
		" || sink: 201 rows/s | 21.0 MiB/s arrow | 2 flushes/s | ~100 msg/s | 90% busy" +
		" || cpu: 250% rss: 1.5 GiB"
	if got != want {
		t.Fatalf("stats line mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestFormatBytesIEC(t *testing.T) {
	tests := []struct {
		bps  float64
		want string
	}{
		{0, "0 B/s"},
		{512, "512 B/s"},
		{1234, "1.2 KiB/s"},
		{5 * 1024 * 1024, "5.0 MiB/s"},
		{1.5 * 1024 * 1024 * 1024, "1.5 GiB/s"},
	}
	for _, tc := range tests {
		if got := formatBytes(tc.bps); got != tc.want {
			t.Errorf("formatBytes(%v) = %q, want %q", tc.bps, got, tc.want)
		}
	}
}

package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/home"
	bolt "go.etcd.io/bbolt"
)

var testNow = time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "data", FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sample(at time.Time, remaining float64) Sample {
	return Sample{At: at, Long: &WindowSample{Remaining: remaining, ResetAt: at.Add(time.Hour).Truncate(time.Second)}, Source: "poll"}
}

func add(t *testing.T, s *Store, account string, samples ...Sample) {
	t.Helper()
	records := make([]SampleRecord, 0, len(samples))
	for _, one := range samples {
		records = append(records, SampleRecord{Provider: "claude", Account: account, Sample: one})
	}
	if err := s.AppendSamples(records); err != nil {
		t.Fatal(err)
	}
}

func TestSecondProcessGetsAClearErrorNotAHang(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	old := openTimeout
	openTimeout = 200 * time.Millisecond
	defer func() { openTimeout = old }()

	started := time.Now()
	second, err := Open(path)
	if err == nil {
		_ = second.Close()
		t.Fatal("a second open of a held database must fail")
	}
	if !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), path) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("second open took %v", elapsed)
	}
}

func TestFileIsOwnerOnly(t *testing.T) {
	if os.PathSeparator != '/' {
		t.Skip("POSIX permissions")
	}
	s := openTemp(t)
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o", perm)
	}
}

func TestSamplesReadBackInOrderAndWithinRange(t *testing.T) {
	s := openTemp(t)
	add(t, s, "a", sample(testNow.Add(-3*time.Hour), 90), sample(testNow.Add(-time.Hour), 70), sample(testNow, 60))
	add(t, s, "b", sample(testNow, 10))

	got, err := s.Samples("claude", "a", testNow.Add(-2*time.Hour), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Long.Remaining != 70 || got[1].Long.Remaining != 60 {
		t.Fatalf("samples = %+v", got)
	}
	if !got[0].At.Equal(testNow.Add(-time.Hour)) || got[0].Long.ResetAt.IsZero() || got[0].Source != "poll" {
		t.Fatalf("decoded sample = %+v", got[0])
	}
	if none, _ := s.Samples("codex", "a", testNow.Add(-time.Hour), testNow); len(none) != 0 {
		t.Fatalf("another provider's series leaked: %+v", none)
	}
	first, ok, err := s.FirstSampleAt("claude", "a")
	if err != nil || !ok || !first.Equal(testNow.Add(-3*time.Hour)) {
		t.Fatalf("first = %v %v %v", first, ok, err)
	}
	if _, ok, _ = s.FirstSampleAt("claude", "nobody"); ok {
		t.Fatal("no series must report no first sample")
	}
}

func TestRetentionKeepsFullDetailThenHourlyThenDrops(t *testing.T) {
	s := openTemp(t)
	day := 24 * time.Hour
	recent := testNow.Add(-10 * day)
	hourlyAge := testNow.Add(-60 * day).Truncate(time.Hour)
	ancient := testNow.Add(-200 * day)
	add(t, s, "a",
		sample(ancient, 5),
		sample(hourlyAge.Add(10*time.Minute), 80),
		sample(hourlyAge.Add(30*time.Minute), 70), // the hour's last reading wins
		sample(hourlyAge.Add(70*time.Minute), 60),
		sample(recent, 50),
		sample(recent.Add(time.Minute), 49),
	)
	if err := s.ApplyRetention(testNow); err != nil {
		t.Fatal(err)
	}
	got, err := s.Samples("claude", "a", testNow.Add(-400*day), testNow)
	if err != nil {
		t.Fatal(err)
	}
	var values []float64
	for _, one := range got {
		values = append(values, one.Long.Remaining)
	}
	want := []float64{70, 60, 50, 49}
	if len(values) != len(want) {
		t.Fatalf("remaining = %v, want %v", values, want)
	}
	for i := range want {
		if values[i] != want[i] {
			t.Fatalf("remaining = %v, want %v", values, want)
		}
	}
	if !got[0].At.Equal(hourlyAge) {
		t.Fatalf("hourly row at %v, want the top of the hour %v", got[0].At, hourlyAge)
	}
}

func TestRetentionDropsEventsAndUsageAndEmptySeries(t *testing.T) {
	s := openTemp(t)
	day := 24 * time.Hour
	old := testNow.Add(-200 * day)
	add(t, s, "gone", sample(old, 5))
	if err := s.AppendEvent(old, map[string]string{"kind": "old"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(testNow, map[string]string{"kind": "new"}); err != nil {
		t.Fatal(err)
	}
	pending := map[string]map[int64]KeyHour{"key": {old.Truncate(time.Hour).Unix(): {Requests: 1}, testNow.Truncate(time.Hour).Unix(): {Requests: 2}}}
	if err := s.AddKeyUsage(pending); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyRetention(testNow); err != nil {
		t.Fatal(err)
	}
	events, err := s.RecentEvents(10)
	if err != nil || len(events) != 1 || !strings.Contains(string(events[0]), "new") {
		t.Fatalf("events = %s err=%v", events, err)
	}
	usage, err := s.KeyUsage(testNow.Add(-400*day), testNow)
	if err != nil || len(usage["key"]) != 1 || usage["key"][0].Requests != 2 {
		t.Fatalf("usage = %+v err=%v", usage, err)
	}
	if err = s.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(bucketSamples).Bucket(seriesName("claude", "gone")) != nil || tx.Bucket(bucketHourly).Bucket(seriesName("claude", "gone")) != nil {
			t.Fatal("an emptied series must be removed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRetentionWorksOffABacklogLargerThanOneBatch(t *testing.T) {
	s := openTemp(t)
	old := testNow.Add(-200 * 24 * time.Hour)
	const total = retentionBatch + 500
	samples := make([]Sample, 0, total)
	for i := 0; i < total; i++ {
		samples = append(samples, sample(old.Add(time.Duration(i)*time.Second), 50))
	}
	add(t, s, "a", samples...)
	if err := s.ApplyRetention(testNow); err != nil {
		t.Fatal(err)
	}
	got, err := s.Samples("claude", "a", old.Add(-time.Hour), testNow)
	if err != nil || len(got) != 0 {
		t.Fatalf("left behind %d samples err=%v", len(got), err)
	}
}

func TestRetentionIsCheapWhenNothingIsDue(t *testing.T) {
	s := openTemp(t)
	samples := make([]Sample, 0, 5000)
	for i := 0; i < 5000; i++ {
		samples = append(samples, sample(testNow.Add(-time.Duration(i)*time.Minute), 50))
	}
	add(t, s, "a", samples...)
	worked, err := s.retentionStep(testNow)
	if err != nil || worked != 0 {
		t.Fatalf("worked %d rows with nothing due, err=%v", worked, err)
	}
}

func TestKeyUsageAccumulatesAcrossFlushes(t *testing.T) {
	s := openTemp(t)
	hour := testNow.Truncate(time.Hour)
	first := map[string]map[int64]KeyHour{"key": {hour.Unix(): {Requests: 1, TotalTokens: 10, LastUsed: 5, Models: map[string]Model{"m": {Requests: 1, TotalTokens: 10}}}}}
	second := map[string]map[int64]KeyHour{"key": {hour.Unix(): {Requests: 2, Failures: 1, TotalTokens: 5, LastUsed: 9, Models: map[string]Model{"m": {Requests: 2, TotalTokens: 5}, "n": {Requests: 1}}}}}
	if err := s.AddKeyUsage(first); err != nil {
		t.Fatal(err)
	}
	if err := s.AddKeyUsage(second); err != nil {
		t.Fatal(err)
	}
	got, err := s.KeyUsage(hour.Add(-time.Minute), hour.Add(time.Minute))
	if err != nil || len(got["key"]) != 1 {
		t.Fatalf("usage = %+v err=%v", got, err)
	}
	row := got["key"][0]
	if row.Requests != 3 || row.Failures != 1 || row.TotalTokens != 15 || row.LastUsed != 9 || row.Models["m"].Requests != 3 || row.Models["n"].Requests != 1 || !row.Hour.Equal(hour) {
		t.Fatalf("row = %+v", row)
	}
}

func TestKVRoundTrip(t *testing.T) {
	s := openTemp(t)
	var out map[string]int
	if ok, err := s.GetJSON("missing", &out); ok || err != nil {
		t.Fatalf("missing: %v %v", ok, err)
	}
	if err := s.PutJSON("k", map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.GetJSON("k", &out); !ok || err != nil || out["a"] != 1 {
		t.Fatalf("got %v %v %v", out, ok, err)
	}
}

func TestEventsNewestFirstAndKeptApartWithinOneMillisecond(t *testing.T) {
	s := openTemp(t)
	for _, kind := range []string{"a", "b", "c"} {
		if err := s.AppendEvent(testNow, map[string]string{"kind": kind}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.RecentEvents(2)
	if err != nil || len(got) != 2 || !strings.Contains(string(got[0]), `"c"`) || !strings.Contains(string(got[1]), `"b"`) {
		t.Fatalf("events = %s err=%v", got, err)
	}
}

func TestBackupIsAValidDatabaseAndRegistersASnapshot(t *testing.T) {
	t.Setenv(home.EnvHome, t.TempDir())
	s, err := OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	add(t, s, "a", sample(testNow, 42))
	fn, ok := tpr.Snapshots()[SnapshotName]
	if !ok {
		t.Fatal("no snapshot registered")
	}
	var buf bytes.Buffer
	if err = fn(&buf); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored.db")
	if err = os.WriteFile(restored, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	copyOf, err := Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = copyOf.Close() }()
	got, err := copyOf.Samples("claude", "a", testNow.Add(-time.Hour), testNow)
	if err != nil || len(got) != 1 || got[0].Long.Remaining != 42 {
		t.Fatalf("restored = %+v err=%v", got, err)
	}
}

func TestPreEpochTimesCannotCorruptOrdering(t *testing.T) {
	if got, ok := keyTime(timeKey(time.Unix(-5, 0))); !ok || got.Unix() != 0 {
		t.Fatalf("clamped key = %v %v", got, ok)
	}
	if _, ok := keyTime([]byte{1, 2}); ok {
		t.Fatal("a short key must not decode")
	}
}

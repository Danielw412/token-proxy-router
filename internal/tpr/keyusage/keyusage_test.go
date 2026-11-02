package keyusage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/connect"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/tpr/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

const (
	keyOne = "sk-test-one-aaaaaaaaaaaaaaaa"
	keyTwo = "sk-test-two-bbbbbbbbbbbbbbbb"
)

var now = time.Date(2026, 10, 26, 12, 30, 0, 0, time.UTC)

func setup(t *testing.T, keys ...string) *Service {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tpr.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &config.Config{}
	cfg.APIKeys = keys
	return New(st, func() *config.Config { return cfg }, func() time.Time { return now })
}

func record(key, model string, at time.Time, input, output, cached int64, failed bool) usage.Record {
	rec := usage.Record{APIKey: key, Model: model, RequestedAt: at, Failed: failed}
	rec.Detail.InputTokens, rec.Detail.OutputTokens, rec.Detail.CachedTokens = input, output, cached
	return rec
}

func TestAggregatesPerKeyWithModelsAndSeries(t *testing.T) {
	s := setup(t, keyOne, keyTwo)
	ctx := context.Background()
	s.HandleUsage(ctx, record(keyOne, "a", now.Add(-2*time.Hour), 10, 5, 2, false))
	s.HandleUsage(ctx, record(keyOne, "b", now.Add(-time.Hour), 100, 50, 0, false))
	s.HandleUsage(ctx, record(keyOne, "b", now, 1, 1, 0, true))
	s.Flush()
	s.HandleUsage(ctx, record(keyOne, "b", now, 1, 1, 0, false)) // same hour, new flush

	keys, err := s.Report(now.Add(-24*time.Hour), now, "hour")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %+v", keys)
	}
	one := keys[0]
	if one.KeyID != KeyID(keyOne) || one.Name != "Key 1" || one.Masked != "sk-...aaaa" {
		t.Fatalf("identity = %+v", one)
	}
	if one.Requests != 4 || one.Failures != 1 || one.InputTokens != 112 || one.OutputTokens != 57 || one.CachedTokens != 2 {
		t.Fatalf("totals = %+v", one)
	}
	// The total is the provider's, not input + output + cached.
	if one.TotalTokens == 0 || one.TotalTokens == one.InputTokens+one.OutputTokens+one.CachedTokens {
		t.Fatalf("total = %+v", one)
	}
	if len(one.Models) != 2 || one.Models[0].Model != "b" || one.Models[0].Requests != 3 || one.Models[1].TotalTokens != 15 {
		t.Fatalf("models = %+v", one.Models)
	}
	if len(one.Series) != 3 || !one.Series[2].At.Equal(now.Truncate(time.Hour)) || one.Series[2].Requests != 2 {
		t.Fatalf("series = %+v", one.Series)
	}
	if one.LastUsedAt == nil || !one.LastUsedAt.Equal(now) {
		t.Fatalf("last used = %v", one.LastUsedAt)
	}
	two := keys[1]
	if two.Requests != 0 || two.LastUsedAt != nil || len(two.Series) != 0 || two.Models == nil {
		t.Fatalf("an unused configured key is still listed, empty: %+v", two)
	}
}

func TestDayBucketsGroupByCalendarDay(t *testing.T) {
	s := setup(t, keyOne)
	ctx := context.Background()
	old := time.Local
	time.Local = time.UTC
	defer func() { time.Local = old }()
	s.HandleUsage(ctx, record(keyOne, "a", now.Add(-26*time.Hour), 1, 0, 0, false))
	s.HandleUsage(ctx, record(keyOne, "a", now.Add(-2*time.Hour), 1, 0, 0, false))
	s.HandleUsage(ctx, record(keyOne, "a", now, 1, 0, 0, false))
	keys, err := s.Report(now.Add(-72*time.Hour), now, "day")
	if err != nil {
		t.Fatal(err)
	}
	series := keys[0].Series
	if len(series) != 2 || series[0].Requests != 1 || series[1].Requests != 2 {
		t.Fatalf("series = %+v", series)
	}
}

func TestDayBucketsStayRightAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	// The 25-hour day DST ends on still makes a single bucket.
	start := time.Date(2026, 11, 1, 0, 30, 0, 0, loc)
	var hours []store.KeyHour
	for i := 0; i < 25; i++ {
		hours = append(hours, store.KeyHour{Hour: start.Add(time.Duration(i) * time.Hour).Truncate(time.Hour), Requests: 1})
	}
	row := buildRow("id", "n", "m", hours, start.Add(-time.Hour), loc)
	if len(row.Series) != 1 || row.Series[0].Requests != 25 {
		t.Fatalf("series = %+v", row.Series)
	}
}

func TestRangeLimitsTheReport(t *testing.T) {
	s := setup(t, keyOne)
	ctx := context.Background()
	s.HandleUsage(ctx, record(keyOne, "a", now.Add(-48*time.Hour), 1, 0, 0, false))
	s.HandleUsage(ctx, record(keyOne, "a", now, 1, 0, 0, false))
	keys, err := s.Report(now.Add(-time.Hour), now, "hour")
	if err != nil || keys[0].Requests != 1 {
		t.Fatalf("keys = %+v err=%v", keys, err)
	}
}

func TestRemovedKeysKeepTheirHistoryWithoutAnyKeyMaterial(t *testing.T) {
	s := setup(t, keyOne)
	s.HandleUsage(context.Background(), record(keyTwo, "a", now, 1, 0, 0, false))
	keys, err := s.Report(now.Add(-time.Hour), now, "hour")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[1].Name != "Removed key" || keys[1].Masked != "" || keys[1].KeyID != KeyID(keyTwo) {
		t.Fatalf("keys = %+v", keys)
	}
}

func TestRawKeyIsNeverStoredOrReturned(t *testing.T) {
	s := setup(t, keyOne)
	s.HandleUsage(context.Background(), record(keyOne, "a", now, 1, 0, 0, false))
	keys, err := s.Report(now.Add(-time.Hour), now, "hour")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(keys)
	if strings.Contains(string(raw), keyOne) {
		t.Fatalf("response has the key: %s", raw)
	}
	if err = s.store.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := os.ReadFile(s.store.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(db), keyOne) {
		t.Fatal("the database holds the raw key")
	}
}

func TestRecordsWithoutAKeyAreIgnored(t *testing.T) {
	s := setup(t)
	s.HandleUsage(context.Background(), record("  ", "a", now, 1, 0, 0, false))
	keys, err := s.Report(now.Add(-time.Hour), now, "hour")
	if err != nil || len(keys) != 0 {
		t.Fatalf("keys = %+v err=%v", keys, err)
	}
}

func TestPendingUsageIsBoundedIfTheDatabaseStops(t *testing.T) {
	s := setup(t, keyOne)
	for i := 0; i < maxPending+50; i++ {
		s.HandleUsage(context.Background(), record(keyOne, "a", now.Add(time.Duration(-i)*time.Hour), 1, 0, 0, false))
	}
	if s.rows > maxPending {
		t.Fatalf("rows = %d", s.rows)
	}
}

func TestKeyIDAndMask(t *testing.T) {
	if KeyID(keyOne) != connect.KeyID(keyOne) || KeyID(keyOne) == KeyID(keyTwo) || strings.Contains(KeyID(keyOne), "aaaa") {
		t.Fatalf("id = %q", KeyID(keyOne))
	}
	if KeyID(keyOne) != KeyID(" "+keyOne+" ") {
		t.Fatal("whitespace must not change the ID")
	}
	if got := Mask(keyOne); got != "sk-...aaaa" {
		t.Fatalf("mask = %q", got)
	}
	if got := Mask("short"); strings.Contains(got, "short") {
		t.Fatalf("a short key must show nothing: %q", got)
	}
}

func TestAPIContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.GET("/usage/keys", handleKeys)
	do := func(path string) (int, map[string]any) {
		w := httptest.NewRecorder()
		e.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		var out map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %q", path, w.Body.String())
		}
		return w.Code, out
	}
	current.Store(nil)
	if code, _ := do("/usage/keys"); code != 503 {
		t.Fatalf("not started: %d", code)
	}
	s := setup(t, keyOne)
	current.Store(s)
	t.Cleanup(func() { current.Store(nil) })
	s.HandleUsage(context.Background(), record(keyOne, "a", now.Add(-time.Minute), 1, 2, 0, false))
	code, out := do("/usage/keys?bucket=day")
	if code != 200 {
		t.Fatalf("keys: %d %v", code, out)
	}
	row := out["keys"].([]any)[0].(map[string]any)
	for _, key := range []string{"key_id", "name", "masked", "requests", "failures", "input_tokens", "output_tokens", "cached_tokens", "last_used_at", "models", "series"} {
		if _, ok := row[key]; !ok {
			t.Errorf("row is missing %q", key)
		}
	}
	for _, query := range []string{"bucket=week", "from=nope", "to=nope", "from=2026-10-27T00:00:00Z"} {
		if code, _ = do("/usage/keys?" + query); code != 400 {
			t.Errorf("%s: %d", query, code)
		}
	}
}

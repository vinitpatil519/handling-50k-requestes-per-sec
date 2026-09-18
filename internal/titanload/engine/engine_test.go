package engine

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type fakeReq struct {
	calls atomic.Uint64
	delay time.Duration
}

func (f *fakeReq) Do(context.Context, *Target) (int, int64, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return 200, 2, nil
}
func (f *fakeReq) Close() {}

func baseConfig() Config {
	return Config{Targets: []Target{{URL: "http://x"}}, Concurrency: 4, Timeout: time.Second}
}

func TestMaxRequestsIsExact(t *testing.T) {
	cfg := baseConfig()
	cfg.MaxRequests = 1000
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	f := &fakeReq{}
	sn := NewWithRequester(cfg, f).Run(context.Background())
	if sn.Requests != 1000 || f.calls.Load() != 1000 {
		t.Fatalf("requests = %d calls = %d", sn.Requests, f.calls.Load())
	}
}

func TestRateModeHoldsRate(t *testing.T) {
	cfg := baseConfig()
	cfg.Rate = 2000
	cfg.Duration = time.Second
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	sn := NewWithRequester(cfg, &fakeReq{}).Run(context.Background())
	if sn.Requests < 1700 || sn.Requests > 2100 {
		t.Fatalf("requests = %d, want ~2000", sn.Requests)
	}
}

func TestScheduleIsInverseOfExpected(t *testing.T) {
	for _, ramp := range []time.Duration{0, 10 * time.Second} {
		for _, n := range []uint64{1, 100, 25_000, 50_000, 200_000} {
			off := ScheduleOffset(10_000, ramp, n)
			back := ExpectedRequests(10_000, ramp, off)
			if math.Abs(back-float64(n)) > 1 {
				t.Errorf("ramp=%v n=%d: offset=%v expected=%v", ramp, n, off, back)
			}
		}
	}
}

func TestHTTPEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	cfg := Config{
		Targets:     []Target{{URL: srv.URL + "/ok"}, {URL: srv.URL + "/fail"}},
		Concurrency: 2, MaxRequests: 200,
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sn := e.Run(context.Background())
	if sn.Requests != 200 || sn.Failures != 100 {
		t.Fatalf("requests=%d failures=%d classes=%v kinds=%v", sn.Requests, sn.Failures, sn.Classes, sn.ErrKinds)
	}
}

func TestParseTargetsFile(t *testing.T) {
	dir := t.TempDir()
	body := filepath.Join(dir, "body.json")
	_ = os.WriteFile(body, []byte(`{"type":"x"}`), 0o600)
	file := filepath.Join(dir, "targets.txt")
	_ = os.WriteFile(file, []byte("# comment\nGET   http://a/1\n\nPOST http://a/2 @"+body+"\nPOST  http://a/3   {\"k\": 1}\n"), 0o600)
	ts, err := ParseTargetsFile(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 3 || ts[0].URL != "http://a/1" || string(ts[1].Body) != `{"type":"x"}` || string(ts[2].Body) != `{"k": 1}` {
		t.Fatalf("targets = %+v", ts)
	}
}

func TestValidate(t *testing.T) {
	c := Config{Targets: []Target{{URL: "ftp://x"}}, Concurrency: 1, Duration: time.Second}
	if err := c.Validate(); err == nil {
		t.Fatal("want error for ftp scheme")
	}
	g := Config{Targets: []Target{{URL: "grpc://localhost:9090"}}, Concurrency: 1, Duration: time.Second}
	if err := g.Validate(); err != nil || g.Mode != ModeGRPC {
		t.Fatalf("grpc autodetect: mode=%s err=%v", g.Mode, err)
	}
}

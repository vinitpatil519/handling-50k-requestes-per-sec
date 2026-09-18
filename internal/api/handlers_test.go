package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/titanedge/titanedge/internal/config"
	"github.com/titanedge/titanedge/internal/events"
	"github.com/titanedge/titanedge/internal/store"
)

type fakeStore struct {
	mu    sync.Mutex
	items map[int64]store.Item
	next  int64
	gets  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{items: map[int64]store.Item{
		1: {ID: 1, Name: "seed", Payload: json.RawMessage(`{}`), CreatedAt: time.Unix(0, 0).UTC()},
	}, next: 2}
}

func (f *fakeStore) GetItem(_ context.Context, id int64) (store.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	it, ok := f.items[id]
	if !ok {
		return it, store.ErrNotFound
	}
	return it, nil
}

func (f *fakeStore) ListItems(_ context.Context, limit int) ([]store.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Item
	for _, it := range f.items {
		if len(out) == limit {
			break
		}
		out = append(out, it)
	}
	return out, nil
}

func (f *fakeStore) CreateItem(_ context.Context, name string, payload json.RawMessage) (store.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	it := store.Item{ID: f.next, Name: name, Payload: payload, CreatedAt: time.Now()}
	f.items[it.ID] = it
	f.next++
	return it, nil
}

func (f *fakeStore) CountItems(context.Context) (int64, error)        { return int64(len(f.items)), nil }
func (f *fakeStore) AddRollups(context.Context, []store.Rollup) error { return nil }
func (f *fakeStore) Ping(context.Context) error                       { return nil }
func (f *fakeStore) Close()                                           {}

type fakeCache struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (c *fakeCache) Get(_ context.Context, k string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.data[k]
	return b, ok, nil
}

func (c *fakeCache) Set(_ context.Context, k string, v []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[k] = v
	return nil
}
func (c *fakeCache) Del(_ context.Context, k string) error          { delete(c.data, k); return nil }
func (c *fakeCache) GetInt(context.Context, string) (int64, error)  { return 42, nil }
func (c *fakeCache) IncrBy(context.Context, map[string]int64) error { return nil }
func (c *fakeCache) Ping(context.Context) error                     { return nil }
func (c *fakeCache) Close() error                                   { return nil }

type fakePublisher struct {
	mu   sync.Mutex
	msgs [][]byte
	err  error
}

func (p *fakePublisher) Publish(_ context.Context, _ string, v []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.msgs = append(p.msgs, v)
	return nil
}
func (p *fakePublisher) Close() {}

func testServer(t *testing.T, deps Deps) (*Server, http.Handler) {
	t.Helper()
	cfg := config.Config{PodName: "test-pod", MaxInflight: 10, MaxBodyBytes: 1 << 20, CacheTTL: time.Minute}
	s := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), deps)
	return s, s.Handler()
}

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPing(t *testing.T) {
	_, h := testServer(t, Deps{})
	rec := do(h, http.MethodGet, "/api/v1/ping", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"pod":"test-pod"`) {
		t.Fatalf("body = %s", rec.Body)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID")
	}
}

func TestDisabledDependenciesReturn503(t *testing.T) {
	_, h := testServer(t, Deps{})
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/v1/items/1", ""},
		{http.MethodGet, "/api/v1/items", ""},
		{http.MethodPost, "/api/v1/items", `{"name":"x"}`},
		{http.MethodPost, "/api/v1/events", `{"type":"click"}`},
	} {
		if rec := do(h, tc.method, tc.path, tc.body); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: code = %d, want 503", tc.method, tc.path, rec.Code)
		}
	}
}

func TestGetItemCacheAside(t *testing.T) {
	st := newFakeStore()
	_, h := testServer(t, Deps{Store: st, Cache: &fakeCache{data: map[string][]byte{}}})

	first := do(h, http.MethodGet, "/api/v1/items/1", "")
	if first.Code != http.StatusOK || first.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("first: code=%d cache=%s", first.Code, first.Header().Get("X-Cache"))
	}
	second := do(h, http.MethodGet, "/api/v1/items/1", "")
	if second.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("second: cache=%s", second.Header().Get("X-Cache"))
	}
	if st.gets != 1 {
		t.Fatalf("store hit %d times, want 1", st.gets)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("cached body differs: %s vs %s", first.Body, second.Body)
	}
}

func TestGetItemValidation(t *testing.T) {
	_, h := testServer(t, Deps{Store: newFakeStore()})
	if rec := do(h, http.MethodGet, "/api/v1/items/abc", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad id: code = %d", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/api/v1/items/999", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing id: code = %d", rec.Code)
	}
}

func TestCreateItemPublishesEvent(t *testing.T) {
	pub := &fakePublisher{}
	_, h := testServer(t, Deps{Store: newFakeStore(), Publisher: pub})
	rec := do(h, http.MethodPost, "/api/v1/items", `{"name":"widget","payload":{"color":"red"}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	if len(pub.msgs) != 1 {
		t.Fatalf("published %d events, want 1", len(pub.msgs))
	}
	var ev events.Event
	if err := json.Unmarshal(pub.msgs[0], &ev); err != nil || ev.Type != "item.created" {
		t.Fatalf("event = %+v err = %v", ev, err)
	}
	if rec := do(h, http.MethodPost, "/api/v1/items", `{"name":""}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty name: code = %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/api/v1/items", `{"name":"a","bogus":1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: code = %d", rec.Code)
	}
}

func TestPublishEvent(t *testing.T) {
	pub := &fakePublisher{}
	_, h := testServer(t, Deps{Publisher: pub})
	if rec := do(h, http.MethodPost, "/api/v1/events", `{"type":"page.view","data":{"p":"/"}}`); rec.Code != http.StatusAccepted {
		t.Fatalf("code = %d body = %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodPost, "/api/v1/events", `{"type":"BAD TYPE"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid type: code = %d", rec.Code)
	}
	pub.err = events.ErrBackpressure
	rec := do(h, http.MethodPost, "/api/v1/events", `{"type":"x"}`)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("backpressure: code = %d", rec.Code)
	}
	pub.err = errors.New("boom")
	if rec := do(h, http.MethodPost, "/api/v1/events", `{"type":"x"}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("publish error: code = %d", rec.Code)
	}
}

func TestLoadShedding(t *testing.T) {
	s, h := testServer(t, Deps{})
	// Fill every admission slot.
	for i := 0; i < cap(s.inflight); i++ {
		s.inflight <- struct{}{}
	}
	if rec := do(h, http.MethodGet, "/api/v1/ping", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rec.Code)
	}
	// Probes must bypass shedding.
	if rec := do(h, http.MethodGet, "/healthz", ""); rec.Code != http.StatusOK {
		t.Fatalf("healthz code = %d", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	fail := true
	_, h := testServer(t, Deps{Checks: map[string]func(context.Context) error{
		"postgres": func(context.Context) error {
			if fail {
				return errors.New("down")
			}
			return nil
		},
	}})
	if rec := do(h, http.MethodGet, "/readyz", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d", rec.Code)
	}
}

func TestStats(t *testing.T) {
	_, h := testServer(t, Deps{Store: newFakeStore(), Cache: &fakeCache{data: map[string][]byte{}}})
	rec := do(h, http.MethodGet, "/api/v1/stats", "")
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["events_processed"].(float64) != 42 || out["items"].(float64) != 1 {
		t.Fatalf("stats = %v", out)
	}
}

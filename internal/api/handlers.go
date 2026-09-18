package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"time"

	"github.com/titanedge/titanedge/internal/buildinfo"
	"github.com/titanedge/titanedge/internal/cache"
	"github.com/titanedge/titanedge/internal/events"
	"github.com/titanedge/titanedge/internal/store"
)

var eventTypeRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ping is the zero-dependency hot path used for raw throughput tests.
func (s *Server) ping(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(s.pingBody)
}

// getItem implements cache-aside: Redis first, PostgreSQL on miss, then
// populate Redis. A Redis failure degrades to a database read.
func (s *Server) getItem(w http.ResponseWriter, r *http.Request) {
	if s.deps.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "database disabled")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, "id must be a positive integer")
		return
	}
	ctx := r.Context()
	key := cache.KeyItemPrefix + strconv.FormatInt(id, 10)

	if s.deps.Cache != nil {
		if b, ok, err := s.deps.Cache.Get(ctx, key); err == nil && ok {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Cache", "HIT")
			_, _ = w.Write(b)
			return
		}
	}

	item, err := s.deps.Store.GetItem(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "item not found")
		return
	case err != nil:
		s.log.ErrorContext(ctx, "get item", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	b, _ := json.Marshal(item)
	if s.deps.Cache != nil {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 250*time.Millisecond)
		_ = s.deps.Cache.Set(cctx, key, b, s.cfg.CacheTTL)
		cancel()
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Cache", "MISS")
	_, _ = w.Write(b)
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	if s.deps.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "database disabled")
		return
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return
		}
		limit = n
	}
	items, err := s.deps.Store.ListItems(r.Context(), limit)
	if err != nil {
		s.log.ErrorContext(r.Context(), "list items", "err", err)
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "count": len(items)})
}

type createItemRequest struct {
	Name    string          `json:"name"`
	Payload json.RawMessage `json:"payload"`
}

func (s *Server) createItem(w http.ResponseWriter, r *http.Request) {
	if s.deps.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "database disabled")
		return
	}
	var req createItemRequest
	if !s.decode(w, r, &req) {
		return
	}
	if n := len([]rune(req.Name)); n < 1 || n > 200 {
		writeError(w, http.StatusBadRequest, "name must be 1-200 characters")
		return
	}
	if len(req.Payload) > 0 && !json.Valid(req.Payload) {
		writeError(w, http.StatusBadRequest, "payload must be valid JSON")
		return
	}
	item, err := s.deps.Store.CreateItem(r.Context(), req.Name, req.Payload)
	if err != nil {
		s.log.ErrorContext(r.Context(), "create item", "err", err)
		writeError(w, http.StatusInternalServerError, "database error")
		return
	}
	if s.deps.Publisher != nil {
		data, _ := json.Marshal(map[string]any{"id": item.ID, "name": item.Name})
		ev, _ := json.Marshal(events.NewEvent("item.created", s.cfg.PodName, data))
		// Best effort: the item is already committed.
		_ = s.deps.Publisher.Publish(r.Context(), "item.created", ev)
	}
	writeJSON(w, http.StatusCreated, item)
}

type publishEventRequest struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// publishEvent is the write-heavy hot path: validate, enqueue to Kafka and
// return 202 without waiting for the broker.
func (s *Server) publishEvent(w http.ResponseWriter, r *http.Request) {
	if s.deps.Publisher == nil {
		writeError(w, http.StatusServiceUnavailable, "event bus disabled")
		return
	}
	var req publishEventRequest
	if !s.decode(w, r, &req) {
		return
	}
	if !eventTypeRe.MatchString(req.Type) {
		writeError(w, http.StatusBadRequest, "type must match "+eventTypeRe.String())
		return
	}
	if len(req.Data) > 0 && !json.Valid(req.Data) {
		writeError(w, http.StatusBadRequest, "data must be valid JSON")
		return
	}
	ev := events.NewEvent(req.Type, s.cfg.PodName, req.Data)
	b, _ := json.Marshal(ev)
	if err := s.deps.Publisher.Publish(r.Context(), req.Type, b); err != nil {
		if errors.Is(err, events.ErrBackpressure) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusServiceUnavailable, "event bus saturated")
			return
		}
		s.log.ErrorContext(r.Context(), "publish event", "err", err)
		writeError(w, http.StatusBadGateway, "publish failed")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": ev.ID, "status": "queued"})
}

// stats feeds the Next.js dashboard.
func (s *Server) stats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := map[string]any{
		"pod":            s.cfg.PodName,
		"version":        buildinfo.Version,
		"uptime_seconds": int64(time.Since(s.started).Seconds()),
		"goroutines":     runtime.NumGoroutine(),
		"requests_local": s.requests.Load(),
		"inflight":       len(s.inflight),
		"max_inflight":   cap(s.inflight),
		"dependencies": map[string]bool{
			"postgres": s.deps.Store != nil,
			"redis":    s.deps.Cache != nil,
			"kafka":    s.deps.Publisher != nil,
		},
	}
	if s.deps.Cache != nil {
		if n, err := s.deps.Cache.GetInt(ctx, cache.KeyEventsTotal); err == nil {
			out["events_processed"] = n
		}
	}
	if s.deps.Store != nil {
		if n, err := s.itemCount(ctx); err == nil {
			out["items"] = n
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// itemCount memoizes the count for 5 seconds per pod.
func (s *Server) itemCount(ctx context.Context) (int64, error) {
	s.countMu.Lock()
	defer s.countMu.Unlock()
	if time.Since(s.countAt) < 5*time.Second {
		return s.count, nil
	}
	n, err := s.deps.Store.CountItems(ctx)
	if err != nil {
		return 0, err
	}
	s.count, s.countAt = n, time.Now()
	return n, nil
}

func (s *Server) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

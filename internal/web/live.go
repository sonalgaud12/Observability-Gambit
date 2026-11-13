package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/sonalgaud12/observability-gambit/internal/game"
)

// Human-mode API. A game is a session; the UI follows its event log.
//
//	POST /api/live/new      {human_color?, script?}  start a game, abandoning any running one
//	POST /api/live/move     {id, move}               play the human's move (UCI "e2e4"/"e7e8q" or SAN)
//	POST /api/live/resign   {id}
//	GET  /api/live/events   ?id=                     server-sent events; resumes from Last-Event-ID
//	GET  /api/live/state    ?id=                     the whole event log as JSON
func (s *server) liveRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/live/new", s.post(s.liveNew))
	mux.HandleFunc("/api/live/move", s.post(s.liveMove))
	mux.HandleFunc("/api/live/resign", s.post(s.liveResign))
	mux.HandleFunc("/api/live/events", s.liveEvents)
	mux.HandleFunc("/api/live/state", s.liveState)
	mux.HandleFunc("/live", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "live.html", http.StatusFound)
	})
}

type apiError struct {
	Error string `json:"error"`
}

func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(apiError{msg})
}

// post wraps a handler: POST only, JSON bodies only, and same-origin only. The
// API starts games that spend model credits, so a page on another origin must
// not be able to drive it just because the stage laptop is listening on
// localhost.
func (s *server) post(h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			fail(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		if o := r.Header.Get("Origin"); o != "" {
			if u, err := url.Parse(o); err != nil || u.Host != r.Host {
				fail(w, http.StatusForbidden, "cross-origin request refused")
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		h(w, r)
	}
}

func (s *server) liveNew(w http.ResponseWriter, r *http.Request) {
	var req struct {
		HumanColor string   `json:"human_color"`
		Script     []string `json:"script"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		fail(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	sess, err := s.opts.Live.Start(game.StartOptions{HumanColor: req.HumanColor, Script: req.Script})
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": sess.ID})
}

func (s *server) liveMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Move string `json:"move"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	sess := s.opts.Live.Session(req.ID)
	if sess == nil {
		fail(w, http.StatusNotFound, "unknown game")
		return
	}
	played, err := sess.Submit(req.Move)
	switch {
	case errors.Is(err, game.ErrNotYourTurn):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, game.ErrIllegalMove):
		fail(w, http.StatusUnprocessableEntity, err.Error())
	case err != nil:
		fail(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, map[string]any{"ok": true, "san": played.SAN, "uci": played.UCI})
	}
}

func (s *server) liveResign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	sess := s.opts.Live.Session(req.ID)
	if sess == nil {
		fail(w, http.StatusNotFound, "unknown game")
		return
	}
	sess.Resign()
	writeJSON(w, map[string]any{"ok": true})
}

func (s *server) liveState(w http.ResponseWriter, r *http.Request) {
	sess := s.opts.Live.Session(r.URL.Query().Get("id"))
	if sess == nil {
		fail(w, http.StatusNotFound, "unknown game")
		return
	}
	evs, _, complete := sess.Events(0)
	writeJSON(w, map[string]any{"id": sess.ID, "complete": complete, "events": evs})
}

// liveEvents streams a session's event log as server-sent events. Every event
// carries its log position as the SSE id, so a browser that drops the
// connection (Wi-Fi, a laptop lid) reconnects with Last-Event-ID and replays
// exactly what it missed: the board cannot silently fall out of step.
func (s *server) liveEvents(w http.ResponseWriter, r *http.Request) {
	sess := s.opts.Live.Session(r.URL.Query().Get("id"))
	if sess == nil {
		fail(w, http.StatusNotFound, "unknown game")
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	after := 0
	if v := r.Header.Get("Last-Event-ID"); v != "" {
		after, _ = strconv.Atoi(v)
	} else if v := r.URL.Query().Get("after"); v != "" {
		after, _ = strconv.Atoi(v)
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, "retry: 1000\n\n")
	fl.Flush()

	ping := time.NewTicker(10 * time.Second)
	defer ping.Stop()
	for {
		evs, more, complete := sess.Events(after)
		for _, ev := range evs {
			fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, ev.Data)
			after = ev.ID
		}
		fl.Flush()
		if complete && len(evs) == 0 {
			return
		}
		if len(evs) > 0 {
			continue // re-read: more may have arrived while writing
		}
		select {
		case <-more:
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

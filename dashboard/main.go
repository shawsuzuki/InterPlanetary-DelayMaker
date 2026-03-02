package main

import (
	"context"
	"embed"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

//go:embed index.html
var indexHTML embed.FS

// ── Redis Keys ──────────────────────────────────────────────────────────────

const (
	configKeyToMars     = "config:delay_to_mars"
	configKeyToEarth    = "config:delay_to_earth"
	configKeyToMoon     = "config:delay_to_moon"
	configKeyFromMoon   = "config:delay_from_moon"
	configKeyToCustom   = "config:delay_to_custom"
	configKeyFromCustom = "config:delay_from_custom"
	queueToMars         = "delay:to_mars"
	queueToEarth        = "delay:to_earth"
	queueToMoon         = "delay:to_moon"
	queueFromMoon       = "delay:from_moon"
	queueToCustom       = "delay:to_custom"
	queueFromCustom     = "delay:from_custom"
)

// configKeyMap maps ramp link names to Redis config keys
var configKeyMap = map[string]string{
	"to_mars":     configKeyToMars,
	"to_earth":    configKeyToEarth,
	"to_moon":     configKeyToMoon,
	"from_moon":   configKeyFromMoon,
	"to_custom":   configKeyToCustom,
	"from_custom": configKeyFromCustom,
}

// ── Types ───────────────────────────────────────────────────────────────────

type Status struct {
	DelayToMars     float64 `json:"delay_to_mars"`
	DelayToEarth    float64 `json:"delay_to_earth"`
	DelayToMoon     float64 `json:"delay_to_moon"`
	DelayFromMoon   float64 `json:"delay_from_moon"`
	DelayToCustom   float64 `json:"delay_to_custom"`
	DelayFromCustom float64 `json:"delay_from_custom"`
	QueueToMars     int64   `json:"queue_to_mars"`
	QueueToEarth    int64   `json:"queue_to_earth"`
	QueueToMoon     int64   `json:"queue_to_moon"`
	QueueFromMoon   int64   `json:"queue_from_moon"`
	QueueToCustom   int64   `json:"queue_to_custom"`
	QueueFromCustom int64   `json:"queue_from_custom"`
	MoonEnabled     bool    `json:"moon_enabled"`
	CustomEnabled   bool    `json:"custom_enabled"`
}

type DelayRequest struct {
	ToMars     float64  `json:"to_mars"`
	ToEarth    float64  `json:"to_earth"`
	ToMoon     *float64 `json:"to_moon,omitempty"`
	FromMoon   *float64 `json:"from_moon,omitempty"`
	ToCustom   *float64 `json:"to_custom,omitempty"`
	FromCustom *float64 `json:"from_custom,omitempty"`
}

type PresetRequest struct {
	Name string `json:"name"`
}

type PresetInfo struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Display string `json:"display"`
}

type RampRequest struct {
	Link     string  `json:"link"`     // "to_mars", "to_earth", "to_custom", "from_custom"
	From     float64 `json:"from"`     // start value (seconds)
	To       float64 `json:"to"`       // end value (seconds)
	Step     float64 `json:"step"`     // delta per tick (can be negative)
	Interval float64 `json:"interval"` // seconds between ticks
}

type RampInfo struct {
	Link    string  `json:"link"`
	Current float64 `json:"current"`
	Target  float64 `json:"target"`
	Step    float64 `json:"step"`
}

// ── Presets ──────────────────────────────────────────────────────────────────
// [toMars, toEarth, toMoon, fromMoon]
// Source: NASA/ESA — Mars closest 54.6Mkm (182s), farthest 401Mkm (1338s), Moon 384400km (1.28s)

var presets = map[string][4]float64{
	"demo":       {5, 5, 1, 1},
	"mars_close": {182, 182, 1.28, 1.28},
	"mars_far":   {1338, 1338, 1.28, 1.28},
}

// ── Ramp State ──────────────────────────────────────────────────────────────

type rampState struct {
	cancel  context.CancelFunc
	current float64
	target  float64
	step    float64
	mu      sync.Mutex
}

var activeRamps sync.Map // map[string]*rampState

// ── Main ────────────────────────────────────────────────────────────────────

func main() {
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	ctx := context.Background()

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Redis connection failed: %v", err)
	}

	http.Handle("/", http.FileServer(http.FS(indexHTML)))

	// ── GET /api/status ─────────────────────────────────────────────────
	http.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		status := Status{}
		if val, err := rdb.Get(ctx, configKeyToMars).Result(); err == nil {
			status.DelayToMars, _ = strconv.ParseFloat(val, 64)
		}
		if val, err := rdb.Get(ctx, configKeyToEarth).Result(); err == nil {
			status.DelayToEarth, _ = strconv.ParseFloat(val, 64)
		}
		if val, err := rdb.Get(ctx, configKeyToMoon).Result(); err == nil {
			status.DelayToMoon, _ = strconv.ParseFloat(val, 64)
			status.MoonEnabled = true
		}
		if val, err := rdb.Get(ctx, configKeyFromMoon).Result(); err == nil {
			status.DelayFromMoon, _ = strconv.ParseFloat(val, 64)
		}
		if val, err := rdb.Get(ctx, configKeyToCustom).Result(); err == nil {
			status.DelayToCustom, _ = strconv.ParseFloat(val, 64)
			status.CustomEnabled = true
		}
		if val, err := rdb.Get(ctx, configKeyFromCustom).Result(); err == nil {
			status.DelayFromCustom, _ = strconv.ParseFloat(val, 64)
		}
		status.QueueToMars = rdb.ZCard(ctx, queueToMars).Val()
		status.QueueToEarth = rdb.ZCard(ctx, queueToEarth).Val()
		status.QueueToMoon = rdb.ZCard(ctx, queueToMoon).Val()
		status.QueueFromMoon = rdb.ZCard(ctx, queueFromMoon).Val()
		status.QueueToCustom = rdb.ZCard(ctx, queueToCustom).Val()
		status.QueueFromCustom = rdb.ZCard(ctx, queueFromCustom).Val()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	})

	// ── POST /api/preset ────────────────────────────────────────────────
	http.HandleFunc("/api/preset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req PresetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		delays, ok := presets[req.Name]
		if !ok {
			http.Error(w, "unknown preset", http.StatusBadRequest)
			return
		}
		setDelay(rdb, ctx, configKeyToMars, delays[0])
		setDelay(rdb, ctx, configKeyToEarth, delays[1])
		setDelay(rdb, ctx, configKeyToMoon, delays[2])
		setDelay(rdb, ctx, configKeyFromMoon, delays[3])
		// Presets don't touch Custom link
		log.Printf("[dashboard] Preset %s: Mars=%.1fs, Moon=%.2fs", req.Name, delays[0], delays[2])
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "preset": req.Name})
	})

	// ── POST /api/delay ─────────────────────────────────────────────────
	http.HandleFunc("/api/delay", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req DelayRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		setDelay(rdb, ctx, configKeyToMars, req.ToMars)
		setDelay(rdb, ctx, configKeyToEarth, req.ToEarth)
		if req.ToMoon != nil {
			setDelay(rdb, ctx, configKeyToMoon, *req.ToMoon)
		}
		if req.FromMoon != nil {
			setDelay(rdb, ctx, configKeyFromMoon, *req.FromMoon)
		}
		if req.ToCustom != nil {
			setDelay(rdb, ctx, configKeyToCustom, *req.ToCustom)
		}
		if req.FromCustom != nil {
			setDelay(rdb, ctx, configKeyFromCustom, *req.FromCustom)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	// ── GET /api/presets ────────────────────────────────────────────────
	http.HandleFunc("/api/presets", func(w http.ResponseWriter, r *http.Request) {
		list := []PresetInfo{
			{Name: "demo", Label: "Demo", Display: "Mars 5s / Moon 1s"},
			{Name: "mars_close", Label: "Mars (closest)", Display: "3m 2s one-way"},
			{Name: "mars_far", Label: "Mars (farthest)", Display: "22m 18s one-way"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})

	// ── POST /api/ramp — Start a delay ramp ─────────────────────────────
	http.HandleFunc("/api/ramp", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleRampStart(w, r, rdb, ctx)
		case http.MethodDelete:
			handleRampStop(w, r)
		case http.MethodGet:
			handleRampStatus(w, r)
		default:
			http.Error(w, "GET/POST/DELETE only", http.StatusMethodNotAllowed)
		}
	})

	log.Println("Dashboard running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func setDelay(rdb *redis.Client, ctx context.Context, key string, secs float64) {
	rdb.Set(ctx, key, strconv.FormatFloat(secs, 'f', -1, 64), 0)
}

// ── Ramp Handlers ───────────────────────────────────────────────────────────

func handleRampStart(w http.ResponseWriter, r *http.Request, rdb *redis.Client, bgCtx context.Context) {
	var req RampRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	redisKey, ok := configKeyMap[req.Link]
	if !ok {
		http.Error(w, "unknown link: "+req.Link, http.StatusBadRequest)
		return
	}
	if req.Interval <= 0 {
		req.Interval = 1
	}
	if req.Step == 0 {
		// Auto-calculate step direction
		if req.To > req.From {
			req.Step = 1
		} else {
			req.Step = -1
		}
	}

	// Cancel existing ramp on this link
	if old, ok := activeRamps.LoadAndDelete(req.Link); ok {
		old.(*rampState).cancel()
	}

	ctx, cancel := context.WithCancel(bgCtx)
	state := &rampState{
		cancel:  cancel,
		current: req.From,
		target:  req.To,
		step:    req.Step,
	}
	activeRamps.Store(req.Link, state)

	// Set initial value
	setDelay(rdb, bgCtx, redisKey, req.From)
	log.Printf("[ramp] Started %s: %.1f → %.1f (step=%.1f, interval=%.1fs)", req.Link, req.From, req.To, req.Step, req.Interval)

	go func() {
		ticker := time.NewTicker(time.Duration(req.Interval * float64(time.Second)))
		defer ticker.Stop()
		defer activeRamps.Delete(req.Link)

		current := req.From
		for {
			select {
			case <-ctx.Done():
				log.Printf("[ramp] Stopped %s at %.1f", req.Link, current)
				return
			case <-ticker.C:
				current += req.Step
				// Check if we've passed the target
				if (req.Step > 0 && current >= req.To) || (req.Step < 0 && current <= req.To) {
					current = req.To
					setDelay(rdb, bgCtx, redisKey, current)
					state.mu.Lock()
					state.current = current
					state.mu.Unlock()
					log.Printf("[ramp] Completed %s: reached %.1f", req.Link, current)
					return
				}
				// Round to avoid floating point drift
				current = math.Round(current*100) / 100
				setDelay(rdb, bgCtx, redisKey, current)
				state.mu.Lock()
				state.current = current
				state.mu.Unlock()
			}
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "link": req.Link})
}

func handleRampStop(w http.ResponseWriter, r *http.Request) {
	link := r.URL.Query().Get("link")
	if link == "" {
		http.Error(w, "link parameter required", http.StatusBadRequest)
		return
	}
	if old, ok := activeRamps.LoadAndDelete(link); ok {
		old.(*rampState).cancel()
		log.Printf("[ramp] Cancelled %s", link)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	} else {
		http.Error(w, "no active ramp for "+link, http.StatusNotFound)
	}
}

func handleRampStatus(w http.ResponseWriter, r *http.Request) {
	var ramps []RampInfo
	activeRamps.Range(func(key, value any) bool {
		s := value.(*rampState)
		s.mu.Lock()
		ramps = append(ramps, RampInfo{
			Link:    key.(string),
			Current: s.current,
			Target:  s.target,
			Step:    s.step,
		})
		s.mu.Unlock()
		return true
	})
	if ramps == nil {
		ramps = []RampInfo{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ramps)
}

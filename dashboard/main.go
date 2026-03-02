package main

import (
	"context"
	"embed"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/redis/go-redis/v9"
)

//go:embed index.html
var indexHTML embed.FS

const (
	configKeyToMars  = "config:delay_to_mars"
	configKeyToEarth = "config:delay_to_earth"
	configKeyToMoon  = "config:delay_to_moon"
	configKeyFromMoon = "config:delay_from_moon"
	queueToMars      = "delay:to_mars"
	queueToEarth     = "delay:to_earth"
	queueToMoon      = "delay:to_moon"
	queueFromMoon    = "delay:from_moon"
)

type Status struct {
	DelayToMars  float64 `json:"delay_to_mars"`
	DelayToEarth float64 `json:"delay_to_earth"`
	DelayToMoon  float64 `json:"delay_to_moon"`
	DelayFromMoon float64 `json:"delay_from_moon"`
	QueueToMars  int64   `json:"queue_to_mars"`
	QueueToEarth int64   `json:"queue_to_earth"`
	QueueToMoon  int64   `json:"queue_to_moon"`
	QueueFromMoon int64  `json:"queue_from_moon"`
	MoonEnabled  bool    `json:"moon_enabled"`
}

type DelayRequest struct {
	ToMars    float64  `json:"to_mars"`
	ToEarth   float64  `json:"to_earth"`
	ToMoon    *float64 `json:"to_moon,omitempty"`
	FromMoon  *float64 `json:"from_moon,omitempty"`
}

type PresetRequest struct {
	Name string `json:"name"`
}

type PresetInfo struct {
	Name    string  `json:"name"`
	Label   string  `json:"label"`
	Display string  `json:"display"`
}

// Presets: [toMars, toEarth, toMoon, fromMoon]
var presets = map[string][4]float64{
	"demo":       {5, 5, 1, 1},
	"moon":       {0, 0, 1.3, 1.3},
	"mars_close": {182, 182, 1.3, 1.3},
	"mars_far":   {1342, 1342, 1.3, 1.3},
}

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
		status.QueueToMars = rdb.ZCard(ctx, queueToMars).Val()
		status.QueueToEarth = rdb.ZCard(ctx, queueToEarth).Val()
		status.QueueToMoon = rdb.ZCard(ctx, queueToMoon).Val()
		status.QueueFromMoon = rdb.ZCard(ctx, queueFromMoon).Val()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	})

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
		setAllDelays(rdb, ctx, delays[0], delays[1], delays[2], delays[3])
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "preset": req.Name})
	})

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
		if req.ToMars < 0 || req.ToEarth < 0 {
			http.Error(w, "delay must be non-negative", http.StatusBadRequest)
			return
		}
		// Set Mars delays
		setDelay(rdb, ctx, configKeyToMars, req.ToMars)
		setDelay(rdb, ctx, configKeyToEarth, req.ToEarth)
		// Set Moon delays if provided
		if req.ToMoon != nil {
			setDelay(rdb, ctx, configKeyToMoon, *req.ToMoon)
		}
		if req.FromMoon != nil {
			setDelay(rdb, ctx, configKeyFromMoon, *req.FromMoon)
		}
		log.Printf("[dashboard] Set delay: Mars=%.1fs/%.1fs", req.ToMars, req.ToEarth)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	http.HandleFunc("/api/presets", func(w http.ResponseWriter, r *http.Request) {
		list := []PresetInfo{
			{Name: "demo", Label: "Demo", Display: "Mars 5s / Moon 1s"},
			{Name: "moon", Label: "Moon Only", Display: "1.3s one-way"},
			{Name: "mars_close", Label: "Mars (closest)", Display: "3m 2s one-way"},
			{Name: "mars_far", Label: "Mars (farthest)", Display: "22m 22s one-way"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})

	log.Println("Dashboard running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func setDelay(rdb *redis.Client, ctx context.Context, key string, secs float64) {
	rdb.Set(ctx, key, strconv.FormatFloat(secs, 'f', -1, 64), 0)
}

func setAllDelays(rdb *redis.Client, ctx context.Context, toMars, toEarth, toMoon, fromMoon float64) {
	setDelay(rdb, ctx, configKeyToMars, toMars)
	setDelay(rdb, ctx, configKeyToEarth, toEarth)
	setDelay(rdb, ctx, configKeyToMoon, toMoon)
	setDelay(rdb, ctx, configKeyFromMoon, fromMoon)
	log.Printf("[dashboard] Set delay: Mars=%.1fs/%.1fs, Moon=%.1fs/%.1fs", toMars, toEarth, toMoon, fromMoon)
}

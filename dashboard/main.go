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
	queueToMars      = "delay:to_mars"
	queueToEarth     = "delay:to_earth"
)

type Status struct {
	DelayToMars  float64 `json:"delay_to_mars"`
	DelayToEarth float64 `json:"delay_to_earth"`
	QueueToMars  int64   `json:"queue_to_mars"`
	QueueToEarth int64   `json:"queue_to_earth"`
}

type DelayRequest struct {
	ToMars  float64 `json:"to_mars"`
	ToEarth float64 `json:"to_earth"`
}

type PresetRequest struct {
	Name string `json:"name"`
}

// Presets: one-way delay in seconds
var presets = map[string][2]float64{
	"moon":       {1.3, 1.3},
	"mars_close": {182, 182},
	"mars_far":   {1342, 1342},
	"demo":       {5, 5},
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
		status.QueueToMars = rdb.ZCard(ctx, queueToMars).Val()
		status.QueueToEarth = rdb.ZCard(ctx, queueToEarth).Val()

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
		setDelays(rdb, ctx, delays[0], delays[1])
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
		setDelays(rdb, ctx, req.ToMars, req.ToEarth)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	http.HandleFunc("/api/presets", func(w http.ResponseWriter, r *http.Request) {
		type PresetInfo struct {
			Name    string  `json:"name"`
			Label   string  `json:"label"`
			Delay   float64 `json:"delay"`
			Display string  `json:"display"`
		}
		list := []PresetInfo{
			{Name: "demo", Label: "Demo", Delay: 5, Display: "5s"},
			{Name: "moon", Label: "Moon", Delay: 1.3, Display: "1.3s"},
			{Name: "mars_close", Label: "Mars (closest)", Delay: 182, Display: "3m 2s"},
			{Name: "mars_far", Label: "Mars (farthest)", Delay: 1342, Display: "22m 22s"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(list)
	})

	log.Println("Dashboard running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func setDelays(rdb *redis.Client, ctx context.Context, toMars, toEarth float64) {
	rdb.Set(ctx, configKeyToMars, strconv.FormatFloat(toMars, 'f', -1, 64), 0)
	rdb.Set(ctx, configKeyToEarth, strconv.FormatFloat(toEarth, 'f', -1, 64), 0)
	log.Printf("[dashboard] Set delay: Earth->Mars=%.1fs, Mars->Earth=%.1fs", toMars, toEarth)
}

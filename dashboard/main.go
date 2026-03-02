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
	configKeyToMars    = "config:delay_to_mars"
	configKeyToEarth   = "config:delay_to_earth"
	configKeyToMoon    = "config:delay_to_moon"
	configKeyFromMoon  = "config:delay_from_moon"
	configKeyToCustom  = "config:delay_to_custom"
	configKeyFromCustom = "config:delay_from_custom"
	queueToMars        = "delay:to_mars"
	queueToEarth       = "delay:to_earth"
	queueToMoon        = "delay:to_moon"
	queueFromMoon      = "delay:from_moon"
	queueToCustom      = "delay:to_custom"
	queueFromCustom    = "delay:from_custom"
)

type Status struct {
	DelayToMars    float64 `json:"delay_to_mars"`
	DelayToEarth   float64 `json:"delay_to_earth"`
	DelayToMoon    float64 `json:"delay_to_moon"`
	DelayFromMoon  float64 `json:"delay_from_moon"`
	DelayToCustom  float64 `json:"delay_to_custom"`
	DelayFromCustom float64 `json:"delay_from_custom"`
	QueueToMars    int64   `json:"queue_to_mars"`
	QueueToEarth   int64   `json:"queue_to_earth"`
	QueueToMoon    int64   `json:"queue_to_moon"`
	QueueFromMoon  int64   `json:"queue_from_moon"`
	QueueToCustom  int64   `json:"queue_to_custom"`
	QueueFromCustom int64  `json:"queue_from_custom"`
	MoonEnabled    bool    `json:"moon_enabled"`
	CustomEnabled  bool    `json:"custom_enabled"`
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

// Presets: [toMars, toEarth, toMoon, fromMoon]
// Source: NASA/ESA — Mars closest 54.6Mkm (182s), farthest 401Mkm (1338s), Moon 384400km (1.28s)
var presets = map[string][4]float64{
	"demo":       {5, 5, 1, 1},
	"moon":       {0, 0, 1.28, 1.28},
	"mars_close": {182, 182, 1.28, 1.28},
	"mars_far":   {1338, 1338, 1.28, 1.28},
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

	http.HandleFunc("/api/presets", func(w http.ResponseWriter, r *http.Request) {
		list := []PresetInfo{
			{Name: "demo", Label: "Demo", Display: "Mars 5s / Moon 1s"},
			{Name: "moon", Label: "Moon Only", Display: "1.28s one-way"},
			{Name: "mars_close", Label: "Mars (closest)", Display: "3m 2s one-way"},
			{Name: "mars_far", Label: "Mars (farthest)", Display: "22m 18s one-way"},
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

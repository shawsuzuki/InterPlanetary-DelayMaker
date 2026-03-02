package main

import (
	"context"
	"embed"
	"encoding/hex"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
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
	// Packet positions: progress + type for visualization dots
	PktsToMars     []PacketDot `json:"pkts_to_mars"`
	PktsToEarth    []PacketDot `json:"pkts_to_earth"`
	PktsToMoon     []PacketDot `json:"pkts_to_moon"`
	PktsFromMoon   []PacketDot `json:"pkts_from_moon"`
	PktsToCustom   []PacketDot `json:"pkts_to_custom"`
	PktsFromCustom []PacketDot `json:"pkts_from_custom"`
}

type PacketDot struct {
	Progress float64 `json:"p"`
	Type     string  `json:"t"` // "arp","icmp","ipv6","tcp","udp","other"
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
	Step     float64 `json:"step"`     // delta per second (amount mode: seconds, rate mode: percent)
	Interval float64 `json:"interval"` // seconds between ticks
	Mode     string  `json:"mode"`     // "amount" (default) or "rate" (percentage)
}

type RampInfo struct {
	Link    string  `json:"link"`
	Current float64 `json:"current"`
	Target  float64 `json:"target"`
	Step    float64 `json:"step"`
}

// ── Presets ──────────────────────────────────────────────────────────────────
// [toMars, toEarth] — presets only affect Mars; Moon stays at 1.28s (fixed)

var presets = map[string][2]float64{
	"demo":       {5, 5},
	"mars_close": {182, 182},
	"mars_far":   {1338, 1338},
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

		// Packet positions (progress 0.0 → 1.0) from Redis sorted set scores
		nowNs := float64(time.Now().UnixNano())
		status.PktsToMars = packetPositions(ctx, rdb, queueToMars, status.DelayToMars, nowNs)
		status.PktsToEarth = packetPositions(ctx, rdb, queueToEarth, status.DelayToEarth, nowNs)
		status.PktsToMoon = packetPositions(ctx, rdb, queueToMoon, status.DelayToMoon, nowNs)
		status.PktsFromMoon = packetPositions(ctx, rdb, queueFromMoon, status.DelayFromMoon, nowNs)
		status.PktsToCustom = packetPositions(ctx, rdb, queueToCustom, status.DelayToCustom, nowNs)
		status.PktsFromCustom = packetPositions(ctx, rdb, queueFromCustom, status.DelayFromCustom, nowNs)

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
		// Presets only affect Mars; Moon (1.28s) and Custom are untouched
		log.Printf("[dashboard] Preset %s: Mars=%.1fs", req.Name, delays[0])
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
			{Name: "demo", Label: "Demo", Display: "Mars 5s"},
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

	// ── POST /api/flush — Clear all packet queues ──────────────────────
	http.HandleFunc("/api/flush", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		queues := []string{
			queueToMars, queueToEarth,
			queueToMoon, queueFromMoon,
			queueToCustom, queueFromCustom,
		}
		deleted := int64(0)
		for _, q := range queues {
			n := rdb.Del(ctx, q).Val()
			deleted += n
		}
		log.Printf("[dashboard] Flushed all queues (%d keys deleted)", deleted)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "deleted": deleted})
	})

	// ── GET /api/pktlog — Recent packet captures per direction ─────────
	http.HandleFunc("/api/pktlog", func(w http.ResponseWriter, r *http.Request) {
		type LogEntry struct {
			Ts   int64  `json:"ts"`
			Size int    `json:"size"`
			Type string `json:"type"`
			Desc string `json:"desc"`
		}
		linkNames := map[string]string{
			"to_mars": "earth→mars", "to_earth": "mars→earth",
			"to_moon": "earth→moon", "from_moon": "moon→earth",
			"to_custom": "earth→custom", "from_custom": "custom→earth",
		}
		result := map[string][]LogEntry{}
		for key, name := range linkNames {
			entries, _ := rdb.LRange(ctx, "pktlog:"+name, 0, 49).Result()
			parsed := make([]LogEntry, 0, len(entries))
			for _, e := range entries {
				parts := strings.SplitN(e, "|", 4)
				if len(parts) == 4 {
					ts, _ := strconv.ParseInt(parts[0], 10, 64)
					size, _ := strconv.Atoi(parts[1])
					parsed = append(parsed, LogEntry{Ts: ts, Size: size, Type: parts[2], Desc: parts[3]})
				}
			}
			result[key] = parsed
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})

	log.Println("Dashboard running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func setDelay(rdb *redis.Client, ctx context.Context, key string, secs float64) {
	rdb.Set(ctx, key, strconv.FormatFloat(secs, 'f', -1, 64), 0)
}

// packetPositions returns progress (0.0→1.0) + packet type for up to 30 in-flight packets.
// Member format: "nanosecond_timestamp:hex_encoded_frame"
func packetPositions(ctx context.Context, rdb *redis.Client, queueKey string, delaySec float64, nowNs float64) []PacketDot {
	if delaySec < 0.001 {
		return []PacketDot{}
	}
	results, err := rdb.ZRangeWithScores(ctx, queueKey, 0, 29).Result()
	if err != nil || len(results) == 0 {
		return []PacketDot{}
	}
	delayNs := delaySec * 1e9
	dots := make([]PacketDot, 0, len(results))
	for _, z := range results {
		remaining := z.Score - nowNs
		if remaining <= 0 {
			continue // already transmitted
		}
		progress := 1.0 - (remaining / delayNs)
		if progress < 0 {
			progress = 0
		}
		progress = math.Round(progress*1000) / 1000
		pktType := classifyMember(z.Member)
		dots = append(dots, PacketDot{Progress: progress, Type: pktType})
	}
	return dots
}

// classifyMember parses the hex-encoded Ethernet frame from a Redis member
// and returns the packet type: "arp","icmp","ipv6","tcp","udp","other"
func classifyMember(member interface{}) string {
	s, ok := member.(string)
	if !ok {
		return "other"
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return "other"
	}
	frame, err := hex.DecodeString(parts[1])
	if err != nil || len(frame) < 14 {
		return "other"
	}
	et := uint16(frame[12])<<8 | uint16(frame[13])
	if et == 0x8100 && len(frame) >= 18 {
		et = uint16(frame[16])<<8 | uint16(frame[17])
	}
	switch et {
	case 0x0806:
		return "arp"
	case 0x86DD:
		return "ipv6"
	case 0x0800:
		if len(frame) >= 24 {
			switch frame[23] {
			case 1:
				return "icmp"
			case 6:
				return "tcp"
			case 17:
				return "udp"
			}
		}
		return "ip"
	}
	return "other"
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
		http.Error(w, "delta must be non-zero", http.StatusBadRequest)
		return
	}
	if req.Mode == "" {
		req.Mode = "amount"
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
	log.Printf("[dynamic] Started %s: %.1f → %.1f (delta=%.2f, mode=%s, interval=%.1fs)",
		req.Link, req.From, req.To, req.Step, req.Mode, req.Interval)

	go func() {
		ticker := time.NewTicker(time.Duration(req.Interval * float64(time.Second)))
		defer ticker.Stop()
		defer activeRamps.Delete(req.Link)

		current := req.From
		for {
			select {
			case <-ctx.Done():
				log.Printf("[dynamic] Stopped %s at %.1f", req.Link, current)
				return
			case <-ticker.C:
				if req.Mode == "rate" {
					current = current * (1 + req.Step/100)
				} else {
					current += req.Step
				}
				// Check if we've passed the target
				if (req.Step > 0 && current >= req.To) || (req.Step < 0 && current <= req.To) {
					current = req.To
					setDelay(rdb, bgCtx, redisKey, current)
					state.mu.Lock()
					state.current = current
					state.mu.Unlock()
					log.Printf("[dynamic] Completed %s: reached %.1f", req.Link, current)
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
	if link == "all" {
		count := 0
		activeRamps.Range(func(key, value any) bool {
			value.(*rampState).cancel()
			activeRamps.Delete(key)
			count++
			return true
		})
		log.Printf("[dynamic] Cleared all (%d stopped)", count)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "stopped": count})
		return
	}
	if old, ok := activeRamps.LoadAndDelete(link); ok {
		old.(*rampState).cancel()
		log.Printf("[dynamic] Cancelled %s", link)
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

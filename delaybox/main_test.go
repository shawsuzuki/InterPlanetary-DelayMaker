package main

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func setupTest(t *testing.T) (*miniredis.Miniredis, *redis.Client, context.Context, context.CancelFunc) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); rdb.Close() })
	return mr, rdb, ctx, cancel
}

func TestNewLink(t *testing.T) {
	l := newLink("test", "q:test", "config:test", 10)
	if got := time.Duration(l.delay.Load()); got != 10*time.Second {
		t.Errorf("delay = %v, want 10s", got)
	}
	if l.name != "test" {
		t.Errorf("name = %q, want %q", l.name, "test")
	}
	if l.queueKey != "q:test" {
		t.Errorf("queueKey = %q, want %q", l.queueKey, "q:test")
	}
}

func TestSetInitialConfig(t *testing.T) {
	mr, rdb, ctx, _ := setupTest(t)
	l := newLink("test", "q:test", "config:test", 10)
	setInitialConfig(ctx, rdb, l)

	val, err := mr.Get("config:test")
	if err != nil {
		t.Fatalf("Redis get: %v", err)
	}
	secs, _ := strconv.ParseFloat(val, 64)
	if secs != 10 {
		t.Errorf("initial config = %v, want 10", secs)
	}
}

func TestReloadDelay_Update(t *testing.T) {
	mr, rdb, ctx, _ := setupTest(t)
	l := newLink("test", "q:test", "config:test", 10)

	mr.Set("config:test", "1200")
	reloadDelay(ctx, rdb, l)

	if got := time.Duration(l.delay.Load()); got != 1200*time.Second {
		t.Errorf("delay = %v, want 1200s", got)
	}
}

func TestReloadDelay_Float(t *testing.T) {
	mr, rdb, ctx, _ := setupTest(t)
	l := newLink("test", "q:test", "config:test", 1)

	mr.Set("config:test", "1.3")
	reloadDelay(ctx, rdb, l)

	got := time.Duration(l.delay.Load())
	want := time.Duration(1.3 * float64(time.Second))
	if got != want {
		t.Errorf("delay = %v, want %v", got, want)
	}
}

func TestReloadDelay_NoKey(t *testing.T) {
	_, rdb, ctx, _ := setupTest(t)
	l := newLink("test", "q:test", "config:test", 10)

	reloadDelay(ctx, rdb, l)

	if got := time.Duration(l.delay.Load()); got != 10*time.Second {
		t.Errorf("delay = %v, want 10s (unchanged)", got)
	}
}

func TestReloadDelay_InvalidValue(t *testing.T) {
	mr, rdb, ctx, _ := setupTest(t)
	l := newLink("test", "q:test", "config:test", 10)

	mr.Set("config:test", "not_a_number")
	reloadDelay(ctx, rdb, l)

	if got := time.Duration(l.delay.Load()); got != 10*time.Second {
		t.Errorf("delay = %v, want 10s (unchanged)", got)
	}
}

func TestReloadDelay_SameValue(t *testing.T) {
	mr, rdb, ctx, _ := setupTest(t)
	l := newLink("test", "q:test", "config:test", 10)

	mr.Set("config:test", "10")
	reloadDelay(ctx, rdb, l)

	if got := time.Duration(l.delay.Load()); got != 10*time.Second {
		t.Errorf("delay = %v, want 10s", got)
	}
}

func TestConfigReloadLoop_PicksUpChanges(t *testing.T) {
	mr, rdb, ctx, _ := setupTest(t)

	toMars := newLink("earth→mars", "q:mars", "config:mars", 10)
	toMoon := newLink("earth→moon", "q:moon", "config:moon", 1)
	mr.Set("config:mars", "10")
	mr.Set("config:moon", "1")

	go configReloadLoop(ctx, rdb, []*link{toMars, toMoon})

	// Change both
	mr.Set("config:mars", "600")
	mr.Set("config:moon", "1.3")
	time.Sleep(1500 * time.Millisecond)

	if got := time.Duration(toMars.delay.Load()); got != 600*time.Second {
		t.Errorf("mars delay = %v, want 600s", got)
	}
	want := time.Duration(1.3 * float64(time.Second))
	if got := time.Duration(toMoon.delay.Load()); got != want {
		t.Errorf("moon delay = %v, want %v", got, want)
	}
}

func TestConfigReloadLoop_StopsOnCancel(t *testing.T) {
	_, rdb, ctx, cancel := setupTest(t)
	l := newLink("test", "q:test", "config:test", 10)

	done := make(chan struct{})
	go func() {
		configReloadLoop(ctx, rdb, []*link{l})
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Error("configReloadLoop did not stop after cancel")
	}
}

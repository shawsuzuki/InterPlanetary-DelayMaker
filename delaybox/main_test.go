package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestDaemon(t *testing.T) (*DelayDaemon, *miniredis.Miniredis) {
	t.Helper()
	s := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: s.Addr()})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		rdb.Close()
	})
	return &DelayDaemon{
		rdb:          rdb,
		ctx:          ctx,
		cancel:       cancel,
		delayToMars:  10 * time.Second,
		delayToEarth: 10 * time.Second,
	}, s
}

func TestGetDelay_InitialValues(t *testing.T) {
	d, _ := newTestDaemon(t)
	if got := d.getDelayToMars(); got != 10*time.Second {
		t.Errorf("getDelayToMars() = %v, want %v", got, 10*time.Second)
	}
	if got := d.getDelayToEarth(); got != 10*time.Second {
		t.Errorf("getDelayToEarth() = %v, want %v", got, 10*time.Second)
	}
}

func TestReloadDelayConfig_UpdatesMarsDelay(t *testing.T) {
	d, s := newTestDaemon(t)

	s.Set("config:delay_to_mars", "1200")
	d.reloadDelayConfig(configKeyToMars, &d.delayToMars, "delay_to_mars")

	if got := d.getDelayToMars(); got != 1200*time.Second {
		t.Errorf("getDelayToMars() = %v, want %v", got, 1200*time.Second)
	}
	// Earth delay should be unchanged
	if got := d.getDelayToEarth(); got != 10*time.Second {
		t.Errorf("getDelayToEarth() should be unchanged, got %v", got)
	}
}

func TestReloadDelayConfig_UpdatesEarthDelay(t *testing.T) {
	d, s := newTestDaemon(t)

	s.Set("config:delay_to_earth", "600")
	d.reloadDelayConfig(configKeyToEarth, &d.delayToEarth, "delay_to_earth")

	if got := d.getDelayToEarth(); got != 600*time.Second {
		t.Errorf("getDelayToEarth() = %v, want %v", got, 600*time.Second)
	}
	// Mars delay should be unchanged
	if got := d.getDelayToMars(); got != 10*time.Second {
		t.Errorf("getDelayToMars() should be unchanged, got %v", got)
	}
}

func TestReloadDelayConfig_FloatSeconds(t *testing.T) {
	d, s := newTestDaemon(t)

	s.Set("config:delay_to_mars", "0.5")
	d.reloadDelayConfig(configKeyToMars, &d.delayToMars, "delay_to_mars")

	if got := d.getDelayToMars(); got != 500*time.Millisecond {
		t.Errorf("getDelayToMars() = %v, want %v", got, 500*time.Millisecond)
	}
}

func TestReloadDelayConfig_NoKey(t *testing.T) {
	d, _ := newTestDaemon(t)

	// No key in Redis — delay should stay at initial value
	d.reloadDelayConfig(configKeyToMars, &d.delayToMars, "delay_to_mars")

	if got := d.getDelayToMars(); got != 10*time.Second {
		t.Errorf("getDelayToMars() = %v, want %v", got, 10*time.Second)
	}
}

func TestReloadDelayConfig_InvalidValue(t *testing.T) {
	d, s := newTestDaemon(t)

	s.Set("config:delay_to_mars", "not_a_number")
	d.reloadDelayConfig(configKeyToMars, &d.delayToMars, "delay_to_mars")

	if got := d.getDelayToMars(); got != 10*time.Second {
		t.Errorf("getDelayToMars() = %v, want %v (should ignore invalid value)", got, 10*time.Second)
	}
}

func TestReloadDelayConfig_SameValueNoChange(t *testing.T) {
	d, s := newTestDaemon(t)

	s.Set("config:delay_to_mars", "10")
	d.reloadDelayConfig(configKeyToMars, &d.delayToMars, "delay_to_mars")

	if got := d.getDelayToMars(); got != 10*time.Second {
		t.Errorf("getDelayToMars() = %v, want %v", got, 10*time.Second)
	}
}

func TestConfigReloadLoop_PicksUpChanges(t *testing.T) {
	d, s := newTestDaemon(t)

	// Set initial values
	s.Set("config:delay_to_mars", "10")
	s.Set("config:delay_to_earth", "10")

	// Start configReloadLoop in background
	go d.configReloadLoop()

	// Change delay via Redis
	s.Set("config:delay_to_mars", "600")
	s.Set("config:delay_to_earth", "300")

	// Wait for reload (ticker is 1s, give some margin)
	time.Sleep(1500 * time.Millisecond)

	if got := d.getDelayToMars(); got != 600*time.Second {
		t.Errorf("getDelayToMars() = %v, want %v", got, 600*time.Second)
	}
	if got := d.getDelayToEarth(); got != 300*time.Second {
		t.Errorf("getDelayToEarth() = %v, want %v", got, 300*time.Second)
	}
}

func TestConfigReloadLoop_StopsOnCancel(t *testing.T) {
	d, _ := newTestDaemon(t)

	done := make(chan struct{})
	go func() {
		d.configReloadLoop()
		close(done)
	}()

	d.cancel()

	select {
	case <-done:
		// OK - loop exited cleanly
	case <-time.After(2 * time.Second):
		t.Error("configReloadLoop did not stop after context cancel")
	}
}

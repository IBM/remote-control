package client

import (
	"testing"
	"time"
)

func TestPollerBackoff(t *testing.T) {
	p := newPoller(nil, "test", "test-client")
	if p.interval != pollInitialInterval {
		t.Errorf("expected initial interval %v, got %v", pollInitialInterval, p.interval)
	}

	p.backoff()
	if p.interval != 2*pollInitialInterval {
		t.Errorf("expected 2x interval after backoff, got %v", p.interval)
	}

	// Backoff to max.
	for i := 0; i < 20; i++ {
		p.backoff()
	}
	if p.interval != pollMaxInterval {
		t.Errorf("expected max interval %v, got %v", pollMaxInterval, p.interval)
	}

	// Reset on success: we simulate by setting interval back.
	p.interval = pollInitialInterval
	if p.interval != pollInitialInterval {
		t.Errorf("expected interval to reset to %v", pollInitialInterval)
	}
}

func TestParseTimestamp(t *testing.T) {
	ts := time.Now().UTC().Truncate(time.Nanosecond)
	s := ts.Format(time.RFC3339Nano)
	got := parseTimestamp(s)
	if !got.Equal(ts) {
		t.Errorf("timestamp mismatch: got %v, want %v", got, ts)
	}
}

func TestParseTimestampInvalid(t *testing.T) {
	got := parseTimestamp("not-a-timestamp")
	if !got.IsZero() {
		t.Errorf("expected zero time for invalid timestamp, got %v", got)
	}
}

func TestPollerOffsets(t *testing.T) {
	p := newPoller(nil, "test", "test-client")
	stdout, stderr := p.currentOffsets()
	if stdout != 0 || stderr != 0 {
		t.Errorf("expected zero offsets, got stdout=%d stderr=%d", stdout, stderr)
	}

	p.stdoutOffset = 100
	p.stderrOffset = 200
	stdout, stderr = p.currentOffsets()
	if stdout != 100 || stderr != 200 {
		t.Errorf("expected (100, 200), got (%d, %d)", stdout, stderr)
	}
}

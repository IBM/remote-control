package client

import (
	"context"
	"log"
	"sort"
	"time"
)

const (
	pollInitialInterval = 500 * time.Millisecond
	pollMaxInterval     = 30 * time.Second
)

// poller polls the server for new output chunks and renders them.
type poller struct {
	client       *APIClient
	sessionID    string
	stdoutOffset int64
	stderrOffset int64
	interval     time.Duration
}

func newPoller(client *APIClient, sessionID string) *poller {
	return &poller{
		client:    client,
		sessionID: sessionID,
		interval:  pollInitialInterval,
	}
}

// run polls until ctx is cancelled, delivering all chunks to renderChunk.
// It applies exponential backoff on failures and resets on success.
func (p *poller) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.interval):
			if err := p.poll(); err != nil {
				log.Printf("[remote-control] poll error: %v", err)
				p.backoff()
			} else {
				p.interval = pollInitialInterval
			}
		}
	}
}

func (p *poller) poll() error {
	result, err := p.client.PollOutput(p.sessionID, p.stdoutOffset, p.stderrOffset)
	if err != nil {
		return err
	}

	// Render chunks sorted by timestamp.
	chunks := result.Chunks
	sort.Slice(chunks, func(i, j int) bool {
		return parseTimestamp(chunks[i].Timestamp).Before(parseTimestamp(chunks[j].Timestamp))
	})
	for _, ch := range chunks {
		renderChunk(ch)
	}

	// Advance offsets.
	if off, ok := result.NextOffsets["stdout"]; ok {
		p.stdoutOffset = off
	}
	if off, ok := result.NextOffsets["stderr"]; ok {
		p.stderrOffset = off
	}
	return nil
}

func (p *poller) backoff() {
	p.interval *= 2
	if p.interval > pollMaxInterval {
		p.interval = pollMaxInterval
	}
}

// currentOffsets returns the current stream offsets.
func (p *poller) currentOffsets() (stdout, stderr int64) {
	return p.stdoutOffset, p.stderrOffset
}

package client

import (
	"context"
	"sort"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
)

const (
	pollInitialInterval = 500 * time.Millisecond
	pollMaxInterval     = 30 * time.Second
)

// poller polls the server for new output chunks and renders them.
type poller struct {
	client       *APIClient
	sessionID    string
	clientID     string
	stdoutOffset int64
	stderrOffset int64
	interval     time.Duration
}

func newPoller(client *APIClient, sessionID, clientID string) *poller {
	return &poller{
		client:    client,
		sessionID: sessionID,
		clientID:  clientID,
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
				ch.Log(alog.DEBUG, "[remote-control] poll error: %v", err)
				p.backoff()
			} else {
				p.interval = pollInitialInterval
			}
		}
	}
}

func (p *poller) poll() error {
	result, err := p.client.PollOutput(p.sessionID, p.clientID, p.stdoutOffset, p.stderrOffset)
	if err != nil {
		return err
	}

	// Check if data was purged (actual offsets differ from requested)
	if actualStdout, ok := result.ActualOffsets["stdout"]; ok && actualStdout > p.stdoutOffset {
		ch.Log(alog.DEBUG, "[remote-control] stdout data purged: requested offset %d, actual offset %d (missed %d bytes)",
			p.stdoutOffset, actualStdout, actualStdout-p.stdoutOffset)
		p.stdoutOffset = actualStdout
	}
	if actualStderr, ok := result.ActualOffsets["stderr"]; ok && actualStderr > p.stderrOffset {
		ch.Log(alog.DEBUG, "[remote-control] stderr data purged: requested offset %d, actual offset %d (missed %d bytes)",
			p.stderrOffset, actualStderr, actualStderr-p.stderrOffset)
		p.stderrOffset = actualStderr
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

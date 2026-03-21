package client

import (
	"context"
	"time"

	"github.com/IBM/alchemy-logging/src/go/alog"
)

const (
	pollInitialInterval = 500 * time.Millisecond
	pollMaxInterval     = 30 * time.Second
)

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

func (p *poller) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(p.interval):
			if err := p.poll(); err != nil {
				ch.Log(alog.DEBUG, "poll error: %v", err)
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

	chunks, ok := result.Elements.([]interface{})
	if !ok {
		return nil
	}

	for _, elem := range chunks {
		if chunkMap, ok := elem.(map[string]interface{}); ok {
			chunk := parseOutputChunk(chunkMap)
			renderChunk(chunk)
		}
	}

	p.stdoutOffset += int64(len(result.Elements.([]interface{})))

	return nil
}

func (p *poller) backoff() {
	p.interval *= 2
	if p.interval > pollMaxInterval {
		p.interval = pollMaxInterval
	}
}

func (p *poller) currentOffsets() (stdout, stderr int64) {
	return p.stdoutOffset, p.stderrOffset
}

// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build linux_bpf

package uploader

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/DataDog/datadog-agent/pkg/util/log"
)

type sender interface {
	send(batch []json.RawMessage) error
}

type effects interface {
	sendBatch(id batchID, items []json.RawMessage)
	resetTimer(timestamp time.Time)
	clearTimer()
}

var _ effects = (*batcher)(nil)

type batchID uint64

type sendResult struct {
	id  batchID
	err error // nil if success, non-nil if failure
}

type batcher struct {
	name          string
	enqueueCh     chan json.RawMessage
	sendResultCh  chan sendResult
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	state         *batcherState
	timer         *time.Timer
	sender        sender
	stopOnce      sync.Once
	errLogLimiter *rate.Limiter
}

func newBatcher(name string, sender sender, batcherConfig batcherConfig) *batcher {
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.NewTimer(0)
	if !timer.Stop() {
		<-timer.C
	}

	b := &batcher{
		name:         name,
		enqueueCh:    make(chan json.RawMessage),
		sendResultCh: make(chan sendResult),
		ctx:          ctx,
		cancel:       cancel,
		state:        newBatcherState(batcherConfig),
		timer:        timer,
		sender:       sender,
		// Used to rate-limit log messages about failed batches.
		//
		// TODO: Keep metrics for the number of failed batches and rate limit
		// the actual attempts to send batches.
		errLogLimiter: rate.NewLimiter(rate.Every(10*time.Second), 1),
	}

	b.wg.Add(1)
	go b.run()
	return b
}

func (b *batcher) enqueue(data json.RawMessage) {
	select {
	case b.enqueueCh <- data:
	case <-b.ctx.Done(): // batcher.run is stopped, drop message
	}
}

func (b *batcher) stop() {
	b.stopOnce.Do(func() {
		log.Debugf("stopping batcher %s", b.name)
		defer log.Debugf("batcher %s stopped", b.name)
		// Cancel the run loop as well as any goroutines trying to signal it.
		b.cancel()

		// Wait for the run loop and any in-flight sender goroutines to finish.
		b.wg.Wait()
	})
}

func (b *batcher) run() {
	defer b.wg.Done()
	defer b.timer.Stop()

	name := any(b.name) // avoid allocating a new string for each log message
	for {
		select {
		case data := <-b.enqueueCh:
			log.Tracef(
				"uploader %s: received enqueue event of %d bytes",
				name, len(data),
			)
			b.state.handleEnqueueEvent(data, time.Now(), b)
		case <-b.timer.C:
			log.Tracef(
				"uploader %s: timer fired event", name,
			)
			if err := b.state.handleTimerFiredEvent(b); err != nil {
				log.Warnf(
					"uploader %s: failed to handle timer fired event: %v",
					name, err,
				)
			}
		case result := <-b.sendResultCh:
			if result.err != nil {
				if b.errLogLimiter.Allow() {
					log.Warnf(
						"uploader %s: batch outcome id=%d: err=%v",
						name, result.id, result.err,
					)
				} else if log.ShouldLog(log.DebugLvl) {
					log.Debugf(
						"uploader %s: batch outcome id=%d: err=%v",
						name, result.id, result.err,
					)
				}
			} else if log.ShouldLog(log.TraceLvl) {
				log.Tracef(
					"uploader %s: batch outcome id=%d: success",
					name, result.id,
				)
			}
			if err := b.state.handleBatchOutcomeEvent(result, b); err != nil {
				log.Warnf(
					"uploader %s: failed to handle batch outcome event: %v",
					name, err,
				)
			}
		case <-b.ctx.Done():
			log.Debugf("uploader %s: received stop event", name)
			b.state.handleStopEvent(b)
			return
		}
	}
}

func (b *batcher) sendBatch(id batchID, items []json.RawMessage) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		res := sendResult{id: id}
		res.err = b.sender.send(items)
		select {
		case b.sendResultCh <- res:
		case <-b.ctx.Done():
		}
	}()
}

func (b *batcher) resetTimer(timestamp time.Time) {
	b.clearTimer()
	b.timer.Reset(time.Until(timestamp))
}

func (b *batcher) clearTimer() {
	if !b.timer.Stop() {
		select {
		case <-b.timer.C:
		default:
		}
	}
}

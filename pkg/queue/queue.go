// Package queue is the job queue on Redis Streams with a consumer group.
//
// Delivery is at least once:
//   - A worker reads a job (XREADGROUP); it is now pending for that worker.
//   - While working, the worker sends heartbeats (XCLAIM JUSTID), which
//     reset the job's idle time without counting as a new delivery.
//   - When the job is done the worker acks and deletes it (XACK + XDEL), so
//     the stream only ever holds jobs that are waiting or in progress.
//   - A job whose worker died stops getting heartbeats; after the idle
//     timeout another worker reclaims it (XCLAIM), which counts a delivery.
//   - A job that reaches the maximum number of deliveries is copied to the
//     dead-letter stream and acked, so it stops blocking the queue.
//
// Redis runs with AOF persistence so queued jobs survive a restart.
package queue

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	FieldJobID   = "job_id"
	FieldPayload = "payload"
)

// Message is one job delivery.
type Message struct {
	// ID is the stream entry id, used to ack and heartbeat.
	ID      string
	JobID   string
	Payload []byte
	// Deliveries counts how many times the job has been handed to a worker,
	// including this one.
	Deliveries int64
}

type Config struct {
	Stream           string
	Group            string
	DeadLetterStream string
}

type Queue struct {
	client redis.UniversalClient
	config Config
}

func New(client redis.UniversalClient, config Config) *Queue {
	return &Queue{client: client, config: config}
}

// EnsureGroup creates the stream and consumer group if they do not exist.
func (q *Queue) EnsureGroup(ctx context.Context) error {
	err := q.client.XGroupCreateMkStream(ctx, q.config.Stream, q.config.Group, "0").Err()
	if err != nil && !strings.HasPrefix(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("create consumer group: %w", err)
	}
	return nil
}

// Read blocks up to block for at most count new jobs for this consumer.
func (q *Queue) Read(ctx context.Context, consumer string, count int64, block time.Duration) ([]Message, error) {
	streams, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    q.config.Group,
		Consumer: consumer,
		Streams:  []string{q.config.Stream, ">"},
		Count:    count,
		Block:    block,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var messages []Message
	for _, stream := range streams {
		for _, entry := range stream.Messages {
			messages = append(messages, toMessage(entry, 1))
		}
	}
	return messages, nil
}

// Heartbeat tells the group this consumer is still working on the job.
func (q *Queue) Heartbeat(ctx context.Context, consumer, id string) error {
	return q.client.XClaimJustID(ctx, &redis.XClaimArgs{
		Stream: q.config.Stream, Group: q.config.Group, Consumer: consumer, MinIdle: 0, Messages: []string{id},
	}).Err()
}

// Ack marks the job done and removes it from the stream.
func (q *Queue) Ack(ctx context.Context, id string) error {
	pipe := q.client.TxPipeline()
	pipe.XAck(ctx, q.config.Stream, q.config.Group, id)
	pipe.XDel(ctx, q.config.Stream, id)
	_, err := pipe.Exec(ctx)
	return err
}

// ReclaimStale takes over jobs whose worker stopped sending heartbeats for
// minIdle. Jobs that have already been delivered maxDeliveries times are
// moved to the dead-letter stream instead and returned in dead, so the
// caller can report them to Core as ISE.
func (q *Queue) ReclaimStale(ctx context.Context, consumer string, minIdle time.Duration, maxDeliveries int64, limit int64) (reclaimed, dead []Message, err error) {
	pending, err := q.client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: q.config.Stream, Group: q.config.Group, Idle: minIdle, Start: "-", End: "+", Count: limit,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("list pending jobs: %w", err)
	}
	for _, entry := range pending {
		if entry.RetryCount >= maxDeliveries {
			message, found, err := q.deadLetter(ctx, consumer, entry, minIdle)
			if err != nil {
				return reclaimed, dead, err
			}
			if found {
				dead = append(dead, message)
			}
			continue
		}
		claimed, err := q.client.XClaim(ctx, &redis.XClaimArgs{
			Stream: q.config.Stream, Group: q.config.Group, Consumer: consumer, MinIdle: minIdle, Messages: []string{entry.ID},
		}).Result()
		if err != nil {
			return reclaimed, dead, fmt.Errorf("claim job %s: %w", entry.ID, err)
		}
		// Empty when another worker claimed it first.
		for _, message := range claimed {
			reclaimed = append(reclaimed, toMessage(message, entry.RetryCount+1))
		}
	}
	return reclaimed, dead, nil
}

func (q *Queue) deadLetter(ctx context.Context, consumer string, entry redis.XPendingExt, minIdle time.Duration) (Message, bool, error) {
	// Claim first so two Judge processes cannot dead-letter the same job.
	claimed, err := q.client.XClaim(ctx, &redis.XClaimArgs{
		Stream: q.config.Stream, Group: q.config.Group, Consumer: consumer, MinIdle: minIdle, Messages: []string{entry.ID},
	}).Result()
	if err != nil {
		return Message{}, false, fmt.Errorf("claim job %s for dead-lettering: %w", entry.ID, err)
	}
	if len(claimed) == 0 {
		// Deleted entry (or claimed by another process): just clear it.
		return Message{}, false, q.client.XAck(ctx, q.config.Stream, q.config.Group, entry.ID).Err()
	}
	message := toMessage(claimed[0], entry.RetryCount)
	pipe := q.client.TxPipeline()
	pipe.XAdd(ctx, &redis.XAddArgs{Stream: q.config.DeadLetterStream, Values: map[string]any{
		FieldJobID:   message.JobID,
		FieldPayload: string(message.Payload),
		"deliveries": strconv.FormatInt(entry.RetryCount, 10),
		"dead_at":    time.Now().UTC().Format(time.RFC3339),
	}})
	pipe.XAck(ctx, q.config.Stream, q.config.Group, entry.ID)
	pipe.XDel(ctx, q.config.Stream, entry.ID)
	if _, err := pipe.Exec(ctx); err != nil {
		return Message{}, false, fmt.Errorf("dead-letter job %s: %w", message.JobID, err)
	}
	return message, true, nil
}

// Stats for monitoring. Because finished jobs are deleted, every entry left
// in the stream is waiting or in progress.
type Stats struct {
	Depth           int64
	OldestAge       time.Duration
	DeadLetterDepth int64
}

func (q *Queue) Stats(ctx context.Context, now time.Time) (Stats, error) {
	pipe := q.client.Pipeline()
	depth := pipe.XLen(ctx, q.config.Stream)
	oldest := pipe.XRangeN(ctx, q.config.Stream, "-", "+", 1)
	dead := pipe.XLen(ctx, q.config.DeadLetterStream)
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return Stats{}, err
	}
	stats := Stats{Depth: depth.Val(), DeadLetterDepth: dead.Val()}
	if entries := oldest.Val(); len(entries) > 0 {
		if createdAt, ok := entryTime(entries[0].ID); ok {
			stats.OldestAge = max(now.Sub(createdAt), 0)
		}
	}
	return stats, nil
}

// entryTime reads the creation time encoded in an auto-generated stream id
// ("<milliseconds>-<sequence>").
func entryTime(id string) (time.Time, bool) {
	milliseconds, _, found := strings.Cut(id, "-")
	if !found {
		return time.Time{}, false
	}
	value, err := strconv.ParseInt(milliseconds, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.UnixMilli(value), true
}

func toMessage(entry redis.XMessage, deliveries int64) Message {
	jobID, _ := entry.Values[FieldJobID].(string)
	payload, _ := entry.Values[FieldPayload].(string)
	return Message{ID: entry.ID, JobID: jobID, Payload: []byte(payload), Deliveries: deliveries}
}

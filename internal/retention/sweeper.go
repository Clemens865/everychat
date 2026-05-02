// Package retention runs the daily DSGVO data-retention sweeper. The
// SQL is in internal/storage.SweepRetention; this package owns the
// scheduling: a goroutine that wakes once a day and invokes the sweep,
// logging the stats. Tests cover the sweep itself in storage; this
// scheduler is small enough to verify by inspection + a single
// manual-trigger entry-point used by both the daemon path and any
// future "run sweep now" admin button.
package retention

import (
	"context"
	"database/sql"
	"log"
	"time"

	"github.com/clemenshoenig/everychat/internal/storage"
)

// DefaultInterval is the cadence of the daily sweep.
const DefaultInterval = 24 * time.Hour

// Scheduler runs SweepRetention periodically.
type Scheduler struct {
	db       *sql.DB
	interval time.Duration
}

// NewScheduler returns a Scheduler with the default 24h cadence.
// Override interval for tests by editing the field directly.
func NewScheduler(db *sql.DB) *Scheduler {
	return &Scheduler{db: db, interval: DefaultInterval}
}

// Start kicks off the scheduler in a goroutine. The returned cancel
// function stops the loop on next tick.
func (s *Scheduler) Start(ctx context.Context) {
	go s.run(ctx)
}

// RunOnce executes a single sweep synchronously. Useful for the
// "run sweep now" admin trigger and for boot-time catch-up — if the
// process was down for a week, RunOnce on startup catches up.
func (s *Scheduler) RunOnce(ctx context.Context) (storage.SweepStats, error) {
	return storage.SweepRetention(ctx, s.db)
}

func (s *Scheduler) run(ctx context.Context) {
	// One catch-up sweep on boot (so an outage doesn't leak retention
	// beyond the bot's configured window). Failure is logged but doesn't
	// kill the daemon — DSGVO sweep failure is loud, not fatal.
	if stats, err := s.RunOnce(ctx); err != nil {
		log.Printf("retention: boot sweep: %v", err)
	} else {
		log.Printf("retention: boot sweep ok — bots=%d chats=%d msgs=%d leads=%d",
			stats.BotsSwept, stats.ChatsDeleted, stats.MessagesDeleted, stats.LeadsDeleted)
	}

	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			stats, err := s.RunOnce(ctx)
			if err != nil {
				log.Printf("retention: tick sweep: %v", err)
				continue
			}
			log.Printf("retention: tick sweep ok — bots=%d chats=%d msgs=%d leads=%d",
				stats.BotsSwept, stats.ChatsDeleted, stats.MessagesDeleted, stats.LeadsDeleted)
		}
	}
}

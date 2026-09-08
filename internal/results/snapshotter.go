// Package results отвечает за чтение агрегатов из Redis и их перенос в Postgres.
package results

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Snapshot — агрегат опроса на момент времени.
type Snapshot struct {
	PollID  uuid.UUID
	Votes   map[uuid.UUID]int64
	Voters  int64 // знаменатель для процентов (architecture.md §5.8)
	TakenAt time.Time
}

// Snapshotter — читающее зеркало батчера.
//
// Симметрия намеренная: батч на запись, батч на чтение, и Redis не видит ни
// 250K записей, ни 250K чтений. Инстанс раз в секунду делает HGETALL по всем
// шардам и складывает результат в атомарный снапшот в памяти
// (architecture.md §5.2).
type Snapshotter struct {
	rdb    redis.UniversalClient
	shards int
	every  time.Duration
	// TODO: atomic.Pointer[map[uuid.UUID]*Snapshot]
}

func NewSnapshotter(rdb redis.UniversalClient, shards int, every time.Duration) *Snapshotter {
	return &Snapshotter{rdb: rdb, shards: shards, every: every}
}

func (s *Snapshotter) Run(ctx context.Context) error {
	// TODO: тикер → для каждого активного опроса собрать shards хешей и
	// shards счётчиков voters, просуммировать, положить в atomic.Pointer.
	return nil
}

// Get возвращает последний снапшот без блокировок. Устарел максимум на
// SNAPSHOT_INTERVAL.
func (s *Snapshotter) Get(pollID uuid.UUID) (*Snapshot, bool) {
	// TODO
	return nil, false
}

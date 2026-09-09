package results

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// Store — запись агрегатов в долговременное хранилище.
//
// Номер прогона (generation) сюда не передаётся: хранилище читает его само, в
// той же команде, из единственного источника истины. Передавать его отсюда
// означало бы полагаться на процессный кэш, который считает опрос неизменяемым,
// — а generation ровно та его часть, которая меняется (architecture.md §5.7).
type Store interface {
	SaveResults(ctx context.Context, pollID uuid.UUID, votes map[uuid.UUID]int64, voters int64) error
}

// Persister переносит снапшоты из Redis в Postgres.
//
// Пишет АБСОЛЮТНОЕ значение счётчика, а не дельту, поэтому повторная запись
// безопасна. Благодаря этому любой инстанс может писать снапшот: выборы лидера,
// распределённые блокировки и выделенный воркер не нужны (architecture.md §5.7).
type Persister struct {
	store Store
	snaps *Snapshotter
	every time.Duration
	log   *slog.Logger
}

func NewPersister(store Store, snaps *Snapshotter, every time.Duration, log *slog.Logger) *Persister {
	return &Persister{store: store, snaps: snaps, every: every, log: log}
}

func (p *Persister) Run(ctx context.Context) {
	t := time.NewTicker(p.every)
	defer t.Stop()

	for {
		select {
		case <-t.C:
			p.Persist(ctx)
		case <-ctx.Done():
			// Финальный перенос: последние секунды события — самые важные, и
			// терять их из-за незакрытого интервала не хочется.
			p.Persist(context.WithoutCancel(ctx))
			return
		}
	}
}

func (p *Persister) Persist(ctx context.Context) {
	for id, snap := range p.snaps.All() {
		if snap.Voters == 0 && len(snap.Votes) == 0 {
			continue
		}
		if err := p.store.SaveResults(ctx, id, snap.Votes, snap.Voters); err != nil {
			p.log.Error("не удалось сохранить агрегаты", "poll_id", id, "err", err)
		}
	}
}

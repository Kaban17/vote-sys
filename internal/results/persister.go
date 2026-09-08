package results

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Persister переносит снапшоты из Redis в Postgres.
//
// Пишет АБСОЛЮТНОЕ значение счётчика, а не дельту, поэтому повторная запись
// безопасна. Благодаря этому любой инстанс может писать снапшот: выборы лидера,
// распределённые блокировки и выделенный воркер не нужны (architecture.md §5.7).
type Persister struct {
	db    *pgxpool.Pool
	snaps *Snapshotter
	every time.Duration
}

func NewPersister(db *pgxpool.Pool, snaps *Snapshotter, every time.Duration) *Persister {
	return &Persister{db: db, snaps: snaps, every: every}
}

func (p *Persister) Run(ctx context.Context) error {
	// TODO: тикер → upsert по всем активным опросам.
	return nil
}

// upsertResults — идемпотентный upsert (architecture.md §5.7).
//
// GREATEST защищает от отката, если два инстанса принесут снапшоты разной
// свежести. Он же делает счётчик принципиально неспособным уменьшаться — отсюда
// generation: при большей генерации значение перезаписывается безусловно.
//
// Это не отладочный костыль. Тот же механизм — единственный корректный способ
// пережить потерю Redis в середине опроса: счётчики обнулились, отсчёт пошёл
// заново, и без generation GREATEST навсегда заморозил бы докризисные цифры.
const upsertResults = `
INSERT INTO poll_results (poll_id, option_id, votes, generation)
VALUES ($1, $2, $3, $4)
ON CONFLICT (poll_id, option_id) DO UPDATE SET
  votes = CASE
    WHEN EXCLUDED.generation > poll_results.generation THEN EXCLUDED.votes
    ELSE GREATEST(poll_results.votes, EXCLUDED.votes)
  END,
  generation = GREATEST(poll_results.generation, EXCLUDED.generation),
  updated_at = now()
`

const upsertTotals = `
INSERT INTO poll_totals (poll_id, voters, generation)
VALUES ($1, $2, $3)
ON CONFLICT (poll_id) DO UPDATE SET
  voters = CASE
    WHEN EXCLUDED.generation > poll_totals.generation THEN EXCLUDED.voters
    ELSE GREATEST(poll_totals.voters, EXCLUDED.voters)
  END,
  generation = GREATEST(poll_totals.generation, EXCLUDED.generation),
  updated_at = now()
`

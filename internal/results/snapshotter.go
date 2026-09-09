package results

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// Snapshot — агрегат опроса на момент времени.
type Snapshot struct {
	PollID uuid.UUID
	// Votes — по идентификатору варианта.
	Votes map[uuid.UUID]int64
	// Voters — число проголосовавших, знаменатель для процентов. При
	// kind = multiple не равно сумме голосов (architecture.md §5.8).
	Voters  int64
	TakenAt time.Time
}

// Sum — сумма голосов по вариантам. Для kind = single обязана совпадать с
// Voters; этот инвариант дёшево ловит ошибки батчера и шардирования.
func (s *Snapshot) Sum() int64 {
	var total int64
	for _, n := range s.Votes {
		total += n
	}
	return total
}

// Tracker сообщает, по каким опросам собирать агрегаты.
type Tracker interface {
	// TrackedIDs возвращает опросы, закончившиеся позже указанного момента:
	// идущие сейчас плюс те, у кого ещё досходятся последние батчи.
	TrackedIDs(after time.Time) []uuid.UUID
}

// Snapshotter — читающее зеркало батчера.
//
// Симметрия намеренная: батч на запись, батч на чтение, и Redis не видит ни
// 250K записей, ни 250K чтений (architecture.md §5.2).
type Snapshotter struct {
	src     Source
	tracker Tracker
	shards  int
	every   time.Duration
	// settle — сколько ещё собирать агрегаты после конца опроса, пока
	// долетают последние батчи со всех инстансов.
	settle time.Duration
	log    *slog.Logger

	// Читатели ходят сюда на каждый запрос к результатам, писатель — раз в
	// секунду, поэтому атомарный указатель на неизменяемую карту вместо
	// блокировок.
	current atomic.Pointer[map[uuid.UUID]*Snapshot]
}

func NewSnapshotter(src Source, tracker Tracker, shards int, every, settle time.Duration, log *slog.Logger) *Snapshotter {
	s := &Snapshotter{src: src, tracker: tracker, shards: shards, every: every, settle: settle, log: log}
	empty := make(map[uuid.UUID]*Snapshot)
	s.current.Store(&empty)
	return s
}

func (s *Snapshotter) Run(ctx context.Context) {
	t := time.NewTicker(s.every)
	defer t.Stop()

	s.Refresh(ctx)
	for {
		select {
		case <-t.C:
			s.Refresh(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// Refresh пересобирает снапшоты всех отслеживаемых опросов.
func (s *Snapshotter) Refresh(ctx context.Context) {
	now := time.Now()
	ids := s.tracker.TrackedIDs(now.Add(-s.settle))

	next := make(map[uuid.UUID]*Snapshot, len(ids))
	prev := *s.current.Load()

	for _, id := range ids {
		raw, err := s.src.Read(ctx, id, s.shards)
		if err != nil {
			// Сохраняем предыдущий снапшот: устаревшие цифры полезнее
			// исчезнувших, а результаты и так eventually consistent.
			if old, ok := prev[id]; ok {
				next[id] = old
			}
			s.log.Error("не удалось прочитать агрегаты", "poll_id", id, "err", err)
			continue
		}

		snap := &Snapshot{PollID: id, Votes: make(map[uuid.UUID]int64, len(raw.Votes)), Voters: raw.Voters, TakenAt: now}
		for optRaw, n := range raw.Votes {
			optID, err := uuid.Parse(optRaw)
			if err != nil {
				continue
			}
			snap.Votes[optID] = n
		}
		next[id] = snap
	}

	s.current.Store(&next)
}

// Get возвращает последний снапшот без блокировок. Устарел максимум на интервал
// обновления.
func (s *Snapshotter) Get(pollID uuid.UUID) (*Snapshot, bool) {
	snap, ok := (*s.current.Load())[pollID]
	return snap, ok
}

// All возвращает все текущие снапшоты — нужен переносу в Postgres.
func (s *Snapshotter) All() map[uuid.UUID]*Snapshot { return *s.current.Load() }

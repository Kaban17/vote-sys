package results

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeSource отдаёт заранее заданные счётчики и умеет отказывать.
type fakeSource struct {
	mu   sync.Mutex
	raw  map[uuid.UUID]Raw
	fail error
	read int
}

func newFakeSource() *fakeSource { return &fakeSource{raw: make(map[uuid.UUID]Raw)} }

func (f *fakeSource) Read(_ context.Context, pollID uuid.UUID, _ int) (Raw, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.read++
	if f.fail != nil {
		return Raw{}, f.fail
	}
	return f.raw[pollID], nil
}

func (f *fakeSource) set(pollID uuid.UUID, votes map[string]int64, voters int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raw[pollID] = Raw{Votes: votes, Voters: voters}
}

func (f *fakeSource) setFail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

type fixedTracker []uuid.UUID

func (t fixedTracker) TrackedIDs(time.Time) []uuid.UUID { return t }

func newSnapshotter(src Source, ids ...uuid.UUID) *Snapshotter {
	return NewSnapshotter(src, fixedTracker(ids), 4, time.Hour, time.Minute, quietLog())
}

func TestRefreshCollectsCounters(t *testing.T) {
	src := newFakeSource()
	pollID := uuid.New()
	optA, optB := uuid.New(), uuid.New()
	src.set(pollID, map[string]int64{optA.String(): 70, optB.String(): 30}, 100)

	s := newSnapshotter(src, pollID)
	s.Refresh(context.Background())

	snap, ok := s.Get(pollID)
	if !ok {
		t.Fatal("снапшот не собран")
	}
	if snap.Votes[optA] != 70 || snap.Votes[optB] != 30 {
		t.Errorf("голоса %v, ожидались 70 и 30", snap.Votes)
	}
	if snap.Voters != 100 {
		t.Errorf("участников %d, ожидалось 100", snap.Voters)
	}
	// Для single сумма обязана совпадать с числом участников
	// (architecture.md §5.8).
	if snap.Sum() != snap.Voters {
		t.Errorf("сумма %d != участников %d", snap.Sum(), snap.Voters)
	}
}

// При ошибке чтения снапшот не должен исчезать: устаревшие цифры полезнее
// пустых, а результаты и так eventually consistent.
func TestRefreshKeepsPreviousOnError(t *testing.T) {
	src := newFakeSource()
	pollID := uuid.New()
	opt := uuid.New()
	src.set(pollID, map[string]int64{opt.String(): 42}, 42)

	s := newSnapshotter(src, pollID)
	s.Refresh(context.Background())

	src.setFail(errors.New("redis недоступен"))
	s.Refresh(context.Background())

	snap, ok := s.Get(pollID)
	if !ok {
		t.Fatal("снапшот исчез после ошибки чтения")
	}
	if snap.Votes[opt] != 42 {
		t.Errorf("голоса %v, ожидались прежние 42", snap.Votes)
	}
}

// Мусорный ключ варианта не должен ронять чтение всего опроса.
func TestRefreshSkipsUnparsableOptionIDs(t *testing.T) {
	src := newFakeSource()
	pollID := uuid.New()
	good := uuid.New()
	src.set(pollID, map[string]int64{good.String(): 5, "не-uuid": 99}, 5)

	s := newSnapshotter(src, pollID)
	s.Refresh(context.Background())

	snap, _ := s.Get(pollID)
	if len(snap.Votes) != 1 || snap.Votes[good] != 5 {
		t.Errorf("голоса %v, ожидался только валидный вариант", snap.Votes)
	}
}

func TestGetIsLockFreeAndAllocationFree(t *testing.T) {
	src := newFakeSource()
	pollID := uuid.New()
	src.set(pollID, map[string]int64{uuid.New().String(): 1}, 1)
	s := newSnapshotter(src, pollID)
	s.Refresh(context.Background())

	if n := testing.AllocsPerRun(100, func() { s.Get(pollID) }); n != 0 {
		t.Errorf("Get выделяет %v аллокаций, ожидалось 0", n)
	}
}

// fakeStore для персистера.
type fakeStore struct {
	mu     sync.Mutex
	saved  map[uuid.UUID]savedResult
	fail   error
	writes int
}

type savedResult struct {
	votes  map[uuid.UUID]int64
	voters int64
}

func newFakeStore() *fakeStore { return &fakeStore{saved: make(map[uuid.UUID]savedResult)} }

func (f *fakeStore) SaveResults(_ context.Context, pollID uuid.UUID, votes map[uuid.UUID]int64, voters int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes++
	if f.fail != nil {
		return f.fail
	}
	f.saved[pollID] = savedResult{votes, voters}
	return nil
}

func TestPersistWritesSnapshot(t *testing.T) {
	src := newFakeSource()
	pollID := uuid.New()
	opt := uuid.New()
	src.set(pollID, map[string]int64{opt.String(): 12}, 12)

	s := newSnapshotter(src, pollID)
	s.Refresh(context.Background())

	store := newFakeStore()
	p := NewPersister(store, s, time.Hour, quietLog())
	p.Persist(context.Background())

	store.mu.Lock()
	defer store.mu.Unlock()
	got, ok := store.saved[pollID]
	if !ok {
		t.Fatal("агрегат не записан")
	}
	if got.voters != 12 || got.votes[opt] != 12 {
		t.Errorf("записано %+v, ожидались 12 и 12", got)
	}
}

// Пустой опрос не должен порождать запись: инстанс без трафика не обязан
// долбить Postgres тикером.
func TestPersistSkipsEmpty(t *testing.T) {
	src := newFakeSource()
	pollID := uuid.New()
	src.set(pollID, map[string]int64{}, 0)

	s := newSnapshotter(src, pollID)
	s.Refresh(context.Background())

	store := newFakeStore()
	NewPersister(store, s, time.Hour, quietLog()).Persist(context.Background())

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.writes != 0 {
		t.Errorf("записей %d, ожидалось 0", store.writes)
	}
}

// Финальный перенос при отмене контекста: последние секунды события — самые
// важные, и терять их из-за незакрытого интервала нельзя.
func TestPersisterFlushesOnShutdown(t *testing.T) {
	src := newFakeSource()
	pollID := uuid.New()
	src.set(pollID, map[string]int64{uuid.New().String(): 9}, 9)

	s := newSnapshotter(src, pollID)
	s.Refresh(context.Background())

	store := newFakeStore()
	p := NewPersister(store, s, time.Hour, quietLog())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run не завершился после отмены")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if store.writes == 0 {
		t.Error("при остановке агрегаты не перенесены")
	}
}

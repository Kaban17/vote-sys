package vote

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

func quietLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeSink — приёмник дельт для тестов: складывает их в память, а по флагу
// отказывает, чтобы проверить возврат несброшенного батча.
type fakeSink struct {
	mu     sync.Mutex
	counts map[string]int64
	voters map[uuid.UUID]int64
	fail   bool
	calls  int
}

func newFakeSink() *fakeSink {
	return &fakeSink{counts: make(map[string]int64), voters: make(map[uuid.UUID]int64)}
}

var errSinkDown = errors.New("приёмник недоступен")

func (f *fakeSink) Push(_ context.Context, pollID uuid.UUID, shard int, optionIDs []string, d Delta) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++

	if f.fail {
		return errSinkDown
	}
	for i, n := range d.Options {
		if n != 0 {
			f.counts[pollID.String()+"|"+optionIDs[i]] += n
		}
	}
	f.voters[pollID] += d.Voters
	return nil
}

func (f *fakeSink) setFail(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = v
}

func (f *fakeSink) option(pollID uuid.UUID, optID string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counts[pollID.String()+"|"+optID]
}

func (f *fakeSink) votersOf(pollID uuid.UUID) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.voters[pollID]
}

func (f *fakeSink) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newTestBatcher(sink Sink) *Batcher {
	// Интервал заведомо больше теста: сброс должен происходить только явным
	// вызовом Flush или отменой контекста.
	return NewBatcher(sink, 0, time.Hour, quietLog())
}

func TestFlushSendsDeltas(t *testing.T) {
	m := newFakeSink()
	b := newTestBatcher(m)

	pollID := uuid.New()
	optA, optB := uuid.New().String(), uuid.New().String()
	c := b.Register(pollID, []string{optA, optB})

	for i := 0; i < 7; i++ {
		c.Add([]int{0})
	}
	for i := 0; i < 3; i++ {
		c.Add([]int{1})
	}

	b.Flush(context.Background())

	if got := m.option(pollID, optA); got != 7 {
		t.Errorf("вариант A = %d, ожидалось 7", got)
	}
	if got := m.option(pollID, optB); got != 3 {
		t.Errorf("вариант B = %d, ожидалось 3", got)
	}
	if got := m.votersOf(pollID); got != 10 {
		t.Errorf("voters = %d, ожидалось 10", got)
	}
}

// Неудачный flush не должен терять голоса: Swap уже забрал их из памяти, и без
// возврата они пропали бы при полностью живом инстансе (architecture.md §5.6).
func TestFailedFlushRestoresAndRetries(t *testing.T) {
	m := newFakeSink()
	b := newTestBatcher(m)

	pollID := uuid.New()
	opt := uuid.New().String()
	c := b.Register(pollID, []string{opt})

	for i := 0; i < 5; i++ {
		c.Add([]int{0})
	}

	m.setFail(true)
	b.Flush(context.Background())

	if got := m.option(pollID, opt); got != 0 {
		t.Fatalf("при отказе в Redis не должно попасть ничего, попало %d", got)
	}

	// Голоса пришли, пока связь была недоступна.
	c.Add([]int{0})

	m.setFail(false)
	b.Flush(context.Background())

	if got := m.option(pollID, opt); got != 6 {
		t.Errorf("после восстановления = %d, ожидалось 6 (5 возвращённых + 1 новый)", got)
	}
}

// Финальный flush при отмене контекста — то, что превращает плановый рестарт в
// нулевую потерю голосов (architecture.md §5.4).
func TestRunFlushesOnShutdown(t *testing.T) {
	m := newFakeSink()
	// Интервал заведомо больше теста: сброс должен произойти именно по отмене.
	b := NewBatcher(m, 0, time.Hour, quietLog())

	pollID := uuid.New()
	opt := uuid.New().String()
	c := b.Register(pollID, []string{opt})
	c.Add([]int{0})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Run(ctx)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run не завершился после отмены контекста")
	}

	if got := m.option(pollID, opt); got != 1 {
		t.Errorf("незасброшенный голос потерян при остановке: %d, ожидался 1", got)
	}
}

// Пустая дельта не должна порождать обращений к Redis: инстанс без трафика не
// обязан долбить хранилище тикером.
func TestFlushSkipsEmptyDeltas(t *testing.T) {
	m := newFakeSink()
	b := newTestBatcher(m)
	b.Register(uuid.New(), []string{uuid.New().String()})

	b.Flush(context.Background())
	b.Flush(context.Background())

	if got := m.callCount(); got != 0 {
		t.Errorf("обращений к приёмнику %d, ожидалось 0", got)
	}
}

// Register идемпотентен, а Lookup не выделяет памяти: он на горячем пути.
func TestRegisterIsIdempotentAndLookupIsFree(t *testing.T) {
	b := newTestBatcher(newFakeSink())
	pollID := uuid.New()
	opts := []string{uuid.New().String(), uuid.New().String()}

	first := b.Register(pollID, opts)
	if second := b.Register(pollID, opts); second != first {
		t.Error("повторный Register должен вернуть те же счётчики")
	}

	got, ok := b.Lookup(pollID)
	if !ok || got != first {
		t.Error("Lookup не нашёл зарегистрированные счётчики")
	}
	if _, ok := b.Lookup(uuid.New()); ok {
		t.Error("Lookup нашёл незарегистрированный опрос")
	}

	if n := testing.AllocsPerRun(100, func() { b.Lookup(pollID) }); n != 0 {
		t.Errorf("Lookup выделяет %v аллокаций, ожидалось 0", n)
	}
}

// Регистрация идёт параллельно голосам: copy-on-write не должен терять записи.
func TestConcurrentRegisterAndLookup(t *testing.T) {
	b := newTestBatcher(newFakeSink())
	ids := make([]uuid.UUID, 32)
	for i := range ids {
		ids[i] = uuid.New()
	}

	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id uuid.UUID) {
			defer wg.Done()
			b.Register(id, []string{uuid.New().String()})
		}(id)
	}
	wg.Wait()

	for _, id := range ids {
		if _, ok := b.Lookup(id); !ok {
			t.Fatalf("опрос %s потерян при параллельной регистрации", id)
		}
	}
}

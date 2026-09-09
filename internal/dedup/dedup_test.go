package dedup

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/boar/vote-sys/internal/breaker"
)

// fakeStore считает обращения — это главное, что нужно проверить: при
// разомкнутой цепи их не должно быть вовсе.
type fakeStore struct {
	mu    sync.Mutex
	keys  map[string]bool
	calls int
	fail  error
	delay time.Duration
}

func newFakeStore() *fakeStore { return &fakeStore{keys: make(map[string]bool)} }

func (f *fakeStore) SetNX(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	f.calls++
	fail, delay := f.fail, f.delay
	f.mu.Unlock()

	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	if fail != nil {
		return false, fail
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.keys[key] {
		return false, nil
	}
	f.keys[key] = true
	return true, nil
}

func (f *fakeStore) setFail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail = err
}

func (f *fakeStore) setDelay(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delay = d
}

func (f *fakeStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newChecker(store Store, threshold int) (*GuardedChecker, *breaker.Breaker) {
	br := breaker.New(breaker.Config{
		ErrorThreshold: threshold,
		Window:         time.Second,
		ProbeInterval:  time.Hour, // в тестах восстановление вызывается явно
	})
	return NewGuardedChecker(store, 50*time.Millisecond, br), br
}

func TestFirstThenDuplicate(t *testing.T) {
	c, _ := newChecker(newFakeStore(), 3)
	pollID := uuid.New()
	ctx := context.Background()

	if got := c.Check(ctx, pollID, "voter-1", time.Minute); got != First {
		t.Errorf("первый голос: %v, ожидался first", got)
	}
	if got := c.Check(ctx, pollID, "voter-1", time.Minute); got != Duplicate {
		t.Errorf("повтор: %v, ожидался duplicate", got)
	}
	// Другой зритель того же опроса не задет.
	if got := c.Check(ctx, pollID, "voter-2", time.Minute); got != First {
		t.Errorf("другой зритель: %v, ожидался first", got)
	}
}

// Токен привязан к опросу, но и ключ дедупа тоже: один зритель может
// голосовать в разных опросах.
func TestKeysAreScopedToPoll(t *testing.T) {
	c, _ := newChecker(newFakeStore(), 3)
	ctx := context.Background()
	a, b := uuid.New(), uuid.New()

	if got := c.Check(ctx, a, "voter", time.Minute); got != First {
		t.Fatalf("опрос A: %v", got)
	}
	if got := c.Check(ctx, b, "voter", time.Minute); got != First {
		t.Errorf("опрос B: %v, ожидался first — дедуп не должен пересекаться между опросами", got)
	}
}

// fail-open: при ошибке хранилища голос принимается, а не отвергается. ТЗ прямо
// называет дедуп обходимым, значит его ценность ниже ценности результата
// (architecture.md §6).
func TestFailOpenOnStoreError(t *testing.T) {
	f := newFakeStore()
	c, _ := newChecker(f, 100) // порог высокий: проверяем именно ошибку, не breaker
	f.setFail(errors.New("redis недоступен"))

	if got := c.Check(context.Background(), uuid.New(), "voter", time.Minute); got != Bypassed {
		t.Errorf("при ошибке хранилища: %v, ожидался bypassed", got)
	}
}

// Таймаут ограничивает урон от ОДНОГО запроса: медленный Redis не должен
// держать хендлер (architecture.md §6).
func TestTimeoutYieldsBypassed(t *testing.T) {
	f := newFakeStore()
	f.setDelay(300 * time.Millisecond) // таймаут чекера — 50 мс
	c, _ := newChecker(f, 100)

	start := time.Now()
	got := c.Check(context.Background(), uuid.New(), "voter", time.Minute)
	elapsed := time.Since(start)

	if got != Bypassed {
		t.Errorf("при таймауте: %v, ожидался bypassed", got)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("ждали %v: таймаут не сработал", elapsed)
	}
}

// САМОЕ ВАЖНОЕ В ШАГЕ: при разомкнутой цепи обращений к хранилищу нет ВООБЩЕ.
// Иначе breaker бесполезен против деградации — каждый запрос всё равно выждал бы
// таймаут, и при 250K RPS это выело бы пул соединений (architecture.md §6).
func TestOpenCircuitMakesNoCalls(t *testing.T) {
	f := newFakeStore()
	c, br := newChecker(f, 3)
	f.setFail(errors.New("redis недоступен"))
	ctx := context.Background()

	// Три ошибки размыкают цепь.
	for i := 0; i < 3; i++ {
		c.Check(ctx, uuid.New(), "voter", time.Minute)
	}
	if br.State() != breaker.Open {
		t.Fatalf("состояние цепи %v, ожидалось open", br.State())
	}

	callsAtOpen := f.callCount()
	for i := 0; i < 500; i++ {
		if got := c.Check(ctx, uuid.New(), "voter", time.Minute); got != Bypassed {
			t.Fatalf("запрос %d: %v, ожидался bypassed", i, got)
		}
	}

	if got := f.callCount(); got != callsAtOpen {
		t.Errorf("после размыкания добавилось %d обращений, ожидалось 0", got-callsAtOpen)
	}
}

// Восстановление: probe проходит, цепь замыкается, дедуп снова работает.
func TestRecoveryAfterProbe(t *testing.T) {
	f := newFakeStore()
	br := breaker.New(breaker.Config{
		ErrorThreshold: 2,
		Window:         time.Second,
		ProbeInterval:  10 * time.Millisecond,
	})
	c := NewGuardedChecker(f, 50*time.Millisecond, br)
	ctx := context.Background()

	f.setFail(errors.New("redis недоступен"))
	c.Check(ctx, uuid.New(), "v", time.Minute)
	c.Check(ctx, uuid.New(), "v", time.Minute)
	if br.State() != breaker.Open {
		t.Fatalf("цепь %v, ожидалось open", br.State())
	}

	f.setFail(nil)
	time.Sleep(20 * time.Millisecond)

	pollID := uuid.New()
	if got := c.Check(ctx, pollID, "voter", time.Minute); got != First {
		t.Fatalf("probe после восстановления: %v, ожидался first", got)
	}
	if br.State() != breaker.Closed {
		t.Errorf("цепь %v, ожидалось closed", br.State())
	}
	if got := c.Check(ctx, pollID, "voter", time.Minute); got != Duplicate {
		t.Errorf("после восстановления дедуп: %v, ожидался duplicate", got)
	}
}

// Под гонкой ровно один из конкурентов получает First: на этом держится вся
// защита от накрутки.
func TestConcurrentCheckHasSingleWinner(t *testing.T) {
	c, _ := newChecker(newFakeStore(), 1000)
	pollID := uuid.New()

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first int
		dup   int
	)
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch c.Check(context.Background(), pollID, "same-voter", time.Minute) {
			case First:
				mu.Lock()
				first++
				mu.Unlock()
			case Duplicate:
				mu.Lock()
				dup++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if first != 1 {
		t.Errorf("First получили %d горутин, ожидалась ровно 1", first)
	}
	if dup != 199 {
		t.Errorf("Duplicate получили %d, ожидалось 199", dup)
	}
}

// hangingStore игнорирует контекст — ровно так вёл себя клиент Redis, когда имя
// переставало резолвиться: запросы висели до 10 секунд при бюджете в 50 мс.
type hangingStore struct{ released chan struct{} }

func (h *hangingStore) SetNX(context.Context, string, time.Duration) (bool, error) {
	<-h.released // не смотрит на ctx вовсе
	return true, nil
}

// Регрессия: чекер обязан вернуться в срок, даже если хранилище не уважает
// контекст. Без сторожевого таймера гарантия «таймаут ограничивает урон от
// одного запроса» держалась только на breaker'е, а он по построению срабатывает
// после порога ошибок и первую партию запросов не защищает (architecture.md §6).
func TestDeadlineHoldsWhenStoreIgnoresContext(t *testing.T) {
	h := &hangingStore{released: make(chan struct{})}
	defer close(h.released)

	c, _ := newChecker(h, 100)

	start := time.Now()
	got := c.Check(context.Background(), uuid.New(), "voter", time.Minute)
	elapsed := time.Since(start)

	if got != Bypassed {
		t.Errorf("результат %v, ожидался bypassed", got)
	}
	if elapsed > 200*time.Millisecond {
		t.Errorf("вызов занял %v при бюджете 50 мс: дедлайн не соблюдён", elapsed)
	}
}

func TestKeyFormat(t *testing.T) {
	id := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	if got, want := Key(id, "abc"), "dedup:11111111-2222-3333-4444-555555555555:abc"; got != want {
		t.Errorf("Key = %q, want %q", got, want)
	}
}

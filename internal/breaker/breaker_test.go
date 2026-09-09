package breaker

import (
	"sync"
	"testing"
	"time"
)

// clock — управляемое время: тесты про интервалы не должны спать.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Unix(1_700_000_000, 0)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestBreaker(cl *clock, threshold int) *Breaker {
	return New(Config{
		ErrorThreshold: threshold,
		Window:         time.Second,
		ProbeInterval:  3 * time.Second,
		Now:            cl.now,
	})
}

func TestClosedByDefault(t *testing.T) {
	b := newTestBreaker(newClock(), 3)
	if b.State() != Closed {
		t.Fatalf("состояние %v, ожидалось closed", b.State())
	}
	if !b.Allow() {
		t.Error("в закрытом состоянии запросы должны проходить")
	}
}

func TestOpensAfterThreshold(t *testing.T) {
	cl := newClock()
	b := newTestBreaker(cl, 3)

	b.Failure()
	b.Failure()
	if b.State() != Closed {
		t.Fatalf("после 2 ошибок из 3 состояние %v, ожидалось closed", b.State())
	}
	if !b.Allow() {
		t.Error("до порога запросы должны проходить")
	}

	b.Failure()
	if b.State() != Open {
		t.Fatalf("после 3 ошибок состояние %v, ожидалось open", b.State())
	}
}

// Главное свойство: разомкнутая цепь не пропускает запросы вовсе. Иначе breaker
// не защищает ни от чего — при 250K RPS каждый запрос выждал бы свой таймаут
// (architecture.md §6).
func TestOpenBlocksEverything(t *testing.T) {
	cl := newClock()
	b := newTestBreaker(cl, 1)
	b.Failure()

	for i := 0; i < 1000; i++ {
		if b.Allow() {
			t.Fatalf("запрос %d прошёл при разомкнутой цепи", i)
		}
	}
}

// Ошибки считаются в скользящем окне: редкие сбои не должны копиться до порога
// за час работы.
func TestWindowResetsErrorCount(t *testing.T) {
	cl := newClock()
	b := newTestBreaker(cl, 3)

	b.Failure()
	b.Failure()

	cl.advance(2 * time.Second) // окно 1 с — истекло

	b.Failure()
	if b.State() != Closed {
		t.Errorf("состояние %v: ошибки из прошлого окна не должны учитываться", b.State())
	}
}

func TestProbeAfterInterval(t *testing.T) {
	cl := newClock()
	b := newTestBreaker(cl, 1)
	b.Failure()

	cl.advance(2 * time.Second) // меньше ProbeInterval
	if b.Allow() {
		t.Fatal("probe не должен пропускаться раньше ProbeInterval")
	}

	cl.advance(2 * time.Second) // суммарно больше ProbeInterval
	if !b.Allow() {
		t.Fatal("после ProbeInterval один probe должен пройти")
	}
	if b.State() != HalfOpen {
		t.Errorf("состояние %v, ожидалось half-open", b.State())
	}

	// Второй запрос в half-open не проходит: probe ровно один.
	if b.Allow() {
		t.Error("в half-open должен проходить только один запрос")
	}
}

func TestSuccessfulProbeCloses(t *testing.T) {
	cl := newClock()
	b := newTestBreaker(cl, 1)
	b.Failure()
	cl.advance(4 * time.Second)

	if !b.Allow() {
		t.Fatal("probe должен пройти")
	}
	b.Success()

	if b.State() != Closed {
		t.Fatalf("состояние %v, ожидалось closed", b.State())
	}
	if !b.Allow() {
		t.Error("после восстановления запросы должны проходить")
	}
}

func TestFailedProbeReopens(t *testing.T) {
	cl := newClock()
	b := newTestBreaker(cl, 1)
	b.Failure()
	cl.advance(4 * time.Second)

	if !b.Allow() {
		t.Fatal("probe должен пройти")
	}
	b.Failure()

	if b.State() != Open {
		t.Fatalf("состояние %v, ожидалось open", b.State())
	}
	// И отсчёт до следующего probe начался заново.
	cl.advance(2 * time.Second)
	if b.Allow() {
		t.Error("после провалившегося probe интервал должен отсчитываться заново")
	}
}

// Ровно один запрос проходит как probe, даже когда претендентов много.
func TestSingleProbeUnderRace(t *testing.T) {
	cl := newClock()
	b := newTestBreaker(cl, 1)
	b.Failure()
	cl.advance(4 * time.Second)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.Allow() {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed != 1 {
		t.Errorf("probe прошли %d запросов, ожидался ровно 1", allowed)
	}
}

// Зависший probe не должен запирать цепь навсегда: если он не вернул ни
// Success, ни Failure, следующий интервал даёт новую попытку.
func TestStuckProbeDoesNotWedge(t *testing.T) {
	cl := newClock()
	b := newTestBreaker(cl, 1)
	b.Failure()

	cl.advance(4 * time.Second)
	if !b.Allow() {
		t.Fatal("первый probe должен пройти")
	}
	// Ответа нет — состояние осталось half-open.

	cl.advance(4 * time.Second)
	if !b.Allow() {
		t.Error("после интервала должен пройти новый probe, даже если предыдущий завис")
	}
}

func TestStateChangeCallback(t *testing.T) {
	cl := newClock()
	var transitions []string
	b := New(Config{
		ErrorThreshold: 1,
		Window:         time.Second,
		ProbeInterval:  time.Second,
		Now:            cl.now,
		OnStateChange: func(from, to State) {
			transitions = append(transitions, from.String()+"→"+to.String())
		},
	})

	b.Failure() // closed → open
	cl.advance(2 * time.Second)
	b.Allow()   // open → half-open
	b.Success() // half-open → closed

	want := []string{"closed→open", "open→half-open", "half-open→closed"}
	if len(transitions) != len(want) {
		t.Fatalf("переходы %v, ожидались %v", transitions, want)
	}
	for i := range want {
		if transitions[i] != want[i] {
			t.Errorf("переход %d: %q, ожидался %q", i, transitions[i], want[i])
		}
	}
}

// Allow вызывается на каждый голос: в норме это должна быть одна атомарная
// загрузка без аллокаций.
func TestAllowDoesNotAllocate(t *testing.T) {
	b := newTestBreaker(newClock(), 10)
	if n := testing.AllocsPerRun(100, func() { b.Allow() }); n != 0 {
		t.Errorf("Allow выделяет %v аллокаций, ожидалось 0", n)
	}
}

func BenchmarkAllowClosed(b *testing.B) {
	br := newTestBreaker(newClock(), 10)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			br.Allow()
		}
	})
}

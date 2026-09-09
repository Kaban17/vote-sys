// Package breaker — circuit breaker для обращений к Redis.
//
// Таймаут и breaker решают разные задачи и нужны оба (architecture.md §6):
//
//   - таймаут ограничивает урон от ОДНОГО запроса; без него медленный Redis
//     держит хендлер, и зритель смотрит на спиннер;
//   - breaker ограничивает урон СИСТЕМНЫЙ. Только таймаута недостаточно: если
//     Redis жив, но отвечает за 200 мс, каждый из 250 000 запросов в секунду
//     честно выждет свои 50 мс, и это мгновенно выест пул соединений и займёт
//     все хендлеры. Breaker после серии ошибок пропускает запросы мимо Redis
//     ВООБЩЕ БЕЗ ПОПЫТКИ, и деградация стоит первых нескольких десятков
//     запросов, а не всего пика.
//
// Состояние локальное для инстанса. Распределённый breaker требовал бы
// обращения к Redis, чтобы решить, стоит ли обращаться к Redis. За 60 секунд
// каждый инстанс независимо придёт к тому же выводу за первые десятки запросов.
package breaker

import (
	"sync/atomic"
	"time"
)

type State int32

const (
	Closed   State = iota // всё хорошо, запросы идут
	Open                  // цепь разомкнута, запросы не идут вовсе
	HalfOpen              // пропускается одиночный probe
)

func (s State) String() string {
	switch s {
	case Closed:
		return "closed"
	case Open:
		return "open"
	default:
		return "half-open"
	}
}

type Config struct {
	// ErrorThreshold — сколько ошибок в окне размыкает цепь.
	ErrorThreshold int
	// Window — скользящее окно подсчёта ошибок.
	Window time.Duration
	// ProbeInterval — как часто пробовать восстановиться.
	ProbeInterval time.Duration

	// Now подменяется в тестах. Пустое значение означает time.Now.
	Now func() time.Time
	// OnStateChange вызывается при смене состояния — событие редкое, поэтому
	// логировать его можно без оглядки на стоимость.
	OnStateChange func(from, to State)
}

type Breaker struct {
	cfg Config

	state atomic.Int32
	// stamp — момент размыкания или начала последнего probe.
	stamp       atomic.Int64
	errs        atomic.Int64
	windowStart atomic.Int64
}

func New(cfg Config) *Breaker {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.ErrorThreshold <= 0 {
		cfg.ErrorThreshold = 1
	}
	b := &Breaker{cfg: cfg}
	b.windowStart.Store(cfg.Now().UnixNano())
	return b
}

// Allow сообщает, стоит ли делать запрос.
//
// В штатном состоянии это одна атомарная загрузка: функция вызывается на каждый
// голос, и всё, что дороже, само стало бы проблемой.
func (b *Breaker) Allow() bool {
	if State(b.state.Load()) == Closed {
		return true
	}

	now := b.cfg.Now().UnixNano()
	st := b.stamp.Load()
	if now-st < int64(b.cfg.ProbeInterval) {
		return false
	}

	// Ровно один запрос проходит как probe: гонку между претендентами решает
	// CAS по метке времени, и он же перезапускает отсчёт, если probe завис и
	// не вернул ни Success, ни Failure.
	if !b.stamp.CompareAndSwap(st, now) {
		return false
	}
	b.setState(HalfOpen)
	return true
}

// Success сообщает об удачном обращении.
func (b *Breaker) Success() {
	if State(b.state.Load()) == Closed {
		return // быстрый путь: в норме Success ничего не стоит
	}
	b.errs.Store(0)
	b.setState(Closed)
}

// Failure сообщает об ошибке или таймауте.
func (b *Breaker) Failure() {
	now := b.cfg.Now().UnixNano()

	if State(b.state.Load()) != Closed {
		// Провалившийся probe снова размыкает цепь на полный интервал.
		b.stamp.Store(now)
		b.setState(Open)
		return
	}

	ws := b.windowStart.Load()
	if now-ws > int64(b.cfg.Window) {
		// Гонка здесь безобидна: если окно сдвинут двое, часть ошибок
		// потеряется, и цепь разомкнётся на пару запросов позже. Точный счёт
		// не нужен — нужно вовремя перестать ходить в неотвечающий Redis.
		if b.windowStart.CompareAndSwap(ws, now) {
			b.errs.Store(0)
		}
	}

	if b.errs.Add(1) >= int64(b.cfg.ErrorThreshold) {
		b.stamp.Store(now)
		b.setState(Open)
	}
}

func (b *Breaker) State() State { return State(b.state.Load()) }

func (b *Breaker) setState(to State) {
	from := State(b.state.Swap(int32(to)))
	if from != to && b.cfg.OnStateChange != nil {
		b.cfg.OnStateChange(from, to)
	}
}

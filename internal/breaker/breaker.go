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

import "time"

type State int

const (
	Closed State = iota // всё хорошо, запросы идут
	Open                // цепь разомкнута, запросы не идут вовсе
	HalfOpen            // пропускается одиночный probe
)

type Config struct {
	// ErrorThreshold — сколько ошибок в окне размыкает цепь.
	ErrorThreshold int
	// Window — скользящее окно подсчёта ошибок.
	Window time.Duration
	// ProbeInterval — как часто пробовать восстановиться.
	ProbeInterval time.Duration
}

type Breaker struct {
	cfg Config
	// TODO: счётчик ошибок в скользящем окне + время размыкания.
}

func New(cfg Config) *Breaker {
	return &Breaker{cfg: cfg}
}

// Allow сообщает, стоит ли делать запрос. Должен быть дешёвым: вызывается на
// каждый голос.
func (b *Breaker) Allow() bool {
	// TODO
	return true
}

func (b *Breaker) Success() {
	// TODO
}

func (b *Breaker) Failure() {
	// TODO
}

func (b *Breaker) State() State {
	// TODO
	return Closed
}

// Package poll содержит доменную модель опроса и её загрузку.
//
// Опрос неизменяем после создания. Это ключевое свойство: метаданные можно
// держать в памяти процесса весь срок жизни опроса и не ходить за ними в Redis
// на каждый голос (architecture.md §3).
package poll

import (
	"time"

	"github.com/google/uuid"
)

type Kind string

const (
	// KindSingle — ровно один вариант. a/b — частный случай с двумя опциями,
	// отдельной сущностью не является.
	KindSingle Kind = "single"
	// KindMultiple — от одного до MaxChoices вариантов.
	KindMultiple Kind = "multiple"
)

type Poll struct {
	ID         uuid.UUID
	Question   string
	Kind       Kind
	MaxChoices int // 0 для KindSingle
	StartsAt   time.Time
	EndsAt     time.Time
	// Generation — номер прогона. Растёт при сбросе состояния Redis и
	// позволяет агрегату в Postgres уменьшиться в обход GREATEST
	// (architecture.md §5.7).
	Generation int
	Options    []Option
}

type Option struct {
	ID       uuid.UUID
	Text     string
	Position int
}

// State — состояние опроса относительно момента t.
type State int

const (
	// StateArmed — опрос создан, но ещё не начался. Инстансы загружают
	// метаданные в этом состоянии, до эфира, а не в первую секунду
	// (architecture.md §6).
	StateArmed State = iota
	StateOpen
	StateClosed
)

// StateAt возвращает состояние опроса. Grace period здесь не участвует: он
// применяется только к приёму голоса, а не к описанию опроса (architecture.md §5.5).
func (p *Poll) StateAt(t time.Time) State {
	// TODO
	return StateClosed
}

// AcceptsVoteAt отвечает, принимается ли голос в момент t с учётом grace period.
// Отсечка по времени приёма, а не по времени обработки: голос, отправленный на
// 59,8-й секунде, доедет после дедлайна, и без запаса режется хвост легитимных
// голосов у зрителей с плохой связью (architecture.md §5.5).
func (p *Poll) AcceptsVoteAt(t time.Time, grace time.Duration) bool {
	// TODO
	return false
}

// ValidateChoice проверяет набор выбранных опций: существование, отсутствие
// дублей, соответствие Kind и MaxChoices. In-memory, без обращений к хранилищам —
// поэтому вызывается до дедупа (architecture.md §3).
func (p *Poll) ValidateChoice(optionIDs []uuid.UUID) error {
	// TODO
	return nil
}

// IndexOf возвращает позицию опции в срезе Options — она же индекс в массиве
// счётчиков (architecture.md §5.6).
func (p *Poll) IndexOf(optionID uuid.UUID) (int, bool) {
	// TODO
	return 0, false
}

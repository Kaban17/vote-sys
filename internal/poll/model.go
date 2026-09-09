// Package poll содержит доменную модель опроса и её загрузку.
//
// Опрос неизменяем после создания. Это ключевое свойство: метаданные можно
// держать в памяти процесса весь срок жизни опроса и не ходить за ними в Redis
// на каждый голос (architecture.md §3).
package poll

import (
	"errors"
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

func (k Kind) Valid() bool { return k == KindSingle || k == KindMultiple }

// Ошибки выбора. Все они означают 400: клиент прислал набор, невозможный для
// этого опроса.
var (
	ErrNoChoice        = errors.New("не выбран ни один вариант")
	ErrDuplicateChoice = errors.New("вариант выбран дважды")
	ErrUnknownOption   = errors.New("такого варианта нет в опросе")
	ErrTooManyChoices  = errors.New("выбрано больше вариантов, чем разрешено")
)

type Poll struct {
	ID         uuid.UUID
	Question   string
	Kind       Kind
	MaxChoices int // 0 — без ограничения (и всегда 0 для KindSingle)
	StartsAt   time.Time
	EndsAt     time.Time
	// Generation — номер прогона. Растёт при сбросе состояния Redis и
	// позволяет агрегату в Postgres уменьшиться в обход GREATEST
	// (architecture.md §5.7).
	Generation int
	// Options упорядочены по Position: индекс в этом срезе — он же индекс в
	// массиве счётчиков (architecture.md §5.6).
	Options []Option
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
	switch {
	case t.Before(p.StartsAt):
		return StateArmed
	case t.Before(p.EndsAt):
		return StateOpen
	default:
		return StateClosed
	}
}

// AcceptsVoteAt отвечает, принимается ли голос в момент t с учётом grace period.
//
// Отсечка по времени приёма, а не по времени обработки: голос, отправленный на
// 59,8-й секунде, доедет после дедлайна, и без запаса режется хвост легитимных
// голосов у зрителей с плохой связью (architecture.md §5.5).
func (p *Poll) AcceptsVoteAt(t time.Time, grace time.Duration) bool {
	if t.Before(p.StartsAt) {
		return false
	}
	return t.Before(p.EndsAt.Add(grace))
}

// ChoiceLimit — сколько вариантов разрешено выбрать.
func (p *Poll) ChoiceLimit() int {
	if p.Kind == KindSingle {
		return 1
	}
	if p.MaxChoices > 0 && p.MaxChoices < len(p.Options) {
		return p.MaxChoices
	}
	return len(p.Options)
}

// ValidateChoice проверяет набор выбранных опций: существование, отсутствие
// дублей, соответствие Kind и MaxChoices. In-memory, без обращений к
// хранилищам — поэтому вызывается до дедупа (architecture.md §3).
//
// Все проверки — вложенными циклами по срезам, без map и без выделения памяти.
// Вариантов 2-10, выбранных обычно 1-3, так что квадратичный обход дешевле
// одной аллокации, а функция лежит на пути каждого голоса.
func (p *Poll) ValidateChoice(optionIDs []uuid.UUID) error {
	if len(optionIDs) == 0 {
		return ErrNoChoice
	}
	if len(optionIDs) > p.ChoiceLimit() {
		return ErrTooManyChoices
	}

	for i, id := range optionIDs {
		for _, earlier := range optionIDs[:i] {
			if earlier == id {
				return ErrDuplicateChoice
			}
		}
		if _, ok := p.IndexOf(id); !ok {
			return ErrUnknownOption
		}
	}
	return nil
}

// IndexOf возвращает позицию опции в срезе Options — она же индекс в массиве
// счётчиков (architecture.md §5.6).
//
// Линейный поиск намеренно: при 2-10 вариантах он быстрее карты и не требует
// её построения при загрузке опроса.
func (p *Poll) IndexOf(optionID uuid.UUID) (int, bool) {
	for i := range p.Options {
		if p.Options[i].ID == optionID {
			return i, true
		}
	}
	return 0, false
}

package poll

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testPoll(kind Kind, maxChoices, numOptions int) *Poll {
	opts := make([]Option, numOptions)
	for i := range opts {
		opts[i] = Option{ID: uuid.New(), Text: string(rune('A' + i)), Position: i}
	}
	start := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	return &Poll{
		ID: uuid.New(), Question: "вопрос", Kind: kind, MaxChoices: maxChoices,
		StartsAt: start, EndsAt: start.Add(time.Minute), Generation: 1, Options: opts,
	}
}

func TestStateAt(t *testing.T) {
	p := testPoll(KindSingle, 0, 2)

	cases := []struct {
		name string
		at   time.Time
		want State
	}{
		{"до начала", p.StartsAt.Add(-time.Second), StateArmed},
		{"ровно в старт", p.StartsAt, StateOpen},
		{"середина", p.StartsAt.Add(30 * time.Second), StateOpen},
		{"ровно в конец", p.EndsAt, StateClosed},
		{"после", p.EndsAt.Add(time.Second), StateClosed},
	}
	for _, c := range cases {
		if got := p.StateAt(c.at); got != c.want {
			t.Errorf("%s: StateAt = %d, want %d", c.name, got, c.want)
		}
	}
}

// Grace period — про приём, а не про состояние. Опрос уже закрыт, но голос,
// отправленный до дедлайна и доехавший после, ещё принимается
// (architecture.md §5.5).
func TestAcceptsVoteAtGracePeriod(t *testing.T) {
	p := testPoll(KindSingle, 0, 2)
	const grace = 3 * time.Second

	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"до начала не принимаем", p.StartsAt.Add(-time.Millisecond), false},
		{"в старт принимаем", p.StartsAt, true},
		{"последняя миллисекунда", p.EndsAt.Add(-time.Millisecond), true},
		{"дедлайн прошёл, но в запасе", p.EndsAt.Add(time.Second), true},
		{"край запаса", p.EndsAt.Add(grace - time.Millisecond), true},
		{"запас исчерпан", p.EndsAt.Add(grace), false},
		{"сильно позже", p.EndsAt.Add(time.Hour), false},
	}
	for _, c := range cases {
		if got := p.AcceptsVoteAt(c.at, grace); got != c.want {
			t.Errorf("%s: AcceptsVoteAt = %v, want %v", c.name, got, c.want)
		}
	}

	// Опрос при этом уже closed: состояние и приём расходятся намеренно.
	if p.StateAt(p.EndsAt.Add(time.Second)) != StateClosed {
		t.Error("после ends_at опрос должен быть closed независимо от grace")
	}
}

func TestValidateChoiceSingle(t *testing.T) {
	p := testPoll(KindSingle, 0, 3)
	other := uuid.New()

	cases := []struct {
		name string
		ids  []uuid.UUID
		want error
	}{
		{"один вариант", []uuid.UUID{p.Options[0].ID}, nil},
		{"пусто", nil, ErrNoChoice},
		{"два при single", []uuid.UUID{p.Options[0].ID, p.Options[1].ID}, ErrTooManyChoices},
		{"несуществующий", []uuid.UUID{other}, ErrUnknownOption},
	}
	for _, c := range cases {
		if err := p.ValidateChoice(c.ids); !errors.Is(err, c.want) {
			t.Errorf("%s: ошибка %v, want %v", c.name, err, c.want)
		}
	}
}

func TestValidateChoiceMultiple(t *testing.T) {
	p := testPoll(KindMultiple, 2, 4)

	cases := []struct {
		name string
		ids  []uuid.UUID
		want error
	}{
		{"один", []uuid.UUID{p.Options[0].ID}, nil},
		{"два — предел", []uuid.UUID{p.Options[0].ID, p.Options[1].ID}, nil},
		{"три — сверх предела", []uuid.UUID{p.Options[0].ID, p.Options[1].ID, p.Options[2].ID}, ErrTooManyChoices},
		{"дубль", []uuid.UUID{p.Options[0].ID, p.Options[0].ID}, ErrDuplicateChoice},
	}
	for _, c := range cases {
		if err := p.ValidateChoice(c.ids); !errors.Is(err, c.want) {
			t.Errorf("%s: ошибка %v, want %v", c.name, err, c.want)
		}
	}
}

// MaxChoices = 0 при multiple означает «без ограничения», то есть предел равен
// числу вариантов.
func TestChoiceLimitUnbounded(t *testing.T) {
	p := testPoll(KindMultiple, 0, 4)
	if got := p.ChoiceLimit(); got != 4 {
		t.Errorf("ChoiceLimit = %d, want 4", got)
	}
	all := []uuid.UUID{p.Options[0].ID, p.Options[1].ID, p.Options[2].ID, p.Options[3].ID}
	if err := p.ValidateChoice(all); err != nil {
		t.Errorf("выбор всех вариантов должен проходить: %v", err)
	}
}

// Индекс в срезе Options — он же индекс в массиве счётчиков
// (architecture.md §5.6), поэтому соответствие должно быть точным.
func TestIndexOfMatchesSliceOrder(t *testing.T) {
	p := testPoll(KindSingle, 0, 5)
	for want, o := range p.Options {
		got, ok := p.IndexOf(o.ID)
		if !ok || got != want {
			t.Errorf("IndexOf(%s) = %d, %v; want %d, true", o.ID, got, ok, want)
		}
	}
	if _, ok := p.IndexOf(uuid.New()); ok {
		t.Error("IndexOf для чужого варианта должен возвращать false")
	}
}

// ValidateChoice лежит на пути каждого голоса: аллокаций в ней быть не должно
// (architecture.md §3).
func TestValidateChoiceDoesNotAllocate(t *testing.T) {
	p := testPoll(KindMultiple, 3, 8)
	ids := []uuid.UUID{p.Options[0].ID, p.Options[3].ID, p.Options[7].ID}

	if n := testing.AllocsPerRun(100, func() { _ = p.ValidateChoice(ids) }); n != 0 {
		t.Errorf("ValidateChoice выделяет %v аллокаций на вызов, ожидалось 0", n)
	}
}

func TestCreateParamsValidate(t *testing.T) {
	start := time.Now()
	valid := func() CreateParams {
		return CreateParams{
			Question: "вопрос", Kind: KindSingle,
			StartsAt: start, EndsAt: start.Add(time.Minute),
			Options: []string{"A", "B"},
		}
	}

	ok := valid()
	if err := ok.Validate(); err != nil {
		t.Fatalf("корректные параметры отвергнуты: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*CreateParams)
	}{
		{"пустой вопрос", func(p *CreateParams) { p.Question = "  " }},
		{"неизвестный kind", func(p *CreateParams) { p.Kind = "ranked" }},
		{"один вариант", func(p *CreateParams) { p.Options = []string{"A"} }},
		{"пустой вариант", func(p *CreateParams) { p.Options = []string{"A", " "} }},
		{"повтор вариантов", func(p *CreateParams) { p.Options = []string{"A", "a"} }},
		{"max_choices при single", func(p *CreateParams) { p.MaxChoices = 2 }},
		{"конец раньше начала", func(p *CreateParams) { p.EndsAt = p.StartsAt.Add(-time.Second) }},
		{"нулевые даты", func(p *CreateParams) { p.StartsAt, p.EndsAt = time.Time{}, time.Time{} }},
		{"max_choices больше вариантов", func(p *CreateParams) {
			p.Kind, p.MaxChoices = KindMultiple, 5
		}},
	}
	for _, c := range cases {
		p := valid()
		c.mutate(&p)
		var invalid *ErrInvalidPoll
		if err := p.Validate(); !errors.As(err, &invalid) {
			t.Errorf("%s: ожидалась ErrInvalidPoll, получено %v", c.name, err)
		}
	}
}

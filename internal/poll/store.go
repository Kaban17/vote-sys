package poll

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound — опроса с таким идентификатором нет.
var ErrNotFound = errors.New("опрос не найден")

// MaxOptions — верхняя граница числа вариантов.
//
// Не техническое ограничение, а следствие двух решений: счётчики лежат
// массивом с линейным поиском по нему (architecture.md §5.6), и вопрос,
// который зритель должен прочитать за минуту эфира, физически не может иметь
// сотню вариантов.
const MaxOptions = 32

// Store — доступ к опросам в Postgres. На горячем пути не используется:
// голосование не касается Postgres вообще (architecture.md §2).
type Store struct {
	db *pgxpool.Pool
}

func NewStore(db *pgxpool.Pool) *Store {
	return &Store{db: db}
}

type CreateParams struct {
	Question   string
	Kind       Kind
	MaxChoices int
	StartsAt   time.Time
	EndsAt     time.Time
	Options    []string
}

// ErrInvalidPoll — ошибка описания опроса при создании.
type ErrInvalidPoll struct{ Field, Reason string }

func (e *ErrInvalidPoll) Error() string { return e.Field + ": " + e.Reason }

func invalid(field, reason string) error { return &ErrInvalidPoll{Field: field, Reason: reason} }

// Validate проверяет параметры до обращения к базе.
//
// Ограничения продублированы в схеме (CHECK'и в миграции), но опираться только
// на них нельзя: нарушение констрейнта прилетело бы как ошибка драйвера, то
// есть 500, тогда как это ошибка запроса и её место — 400.
func (p *CreateParams) Validate() error {
	if strings.TrimSpace(p.Question) == "" {
		return invalid("question", "пустой вопрос")
	}
	if !p.Kind.Valid() {
		return invalid("kind", `допустимо "single" или "multiple"`)
	}
	if len(p.Options) < 2 {
		return invalid("options", "нужно минимум два варианта")
	}
	if len(p.Options) > MaxOptions {
		return invalid("options", fmt.Sprintf("не больше %d вариантов", MaxOptions))
	}
	for i, o := range p.Options {
		if strings.TrimSpace(o) == "" {
			return invalid("options", fmt.Sprintf("вариант %d пустой", i+1))
		}
		for _, earlier := range p.Options[:i] {
			if strings.EqualFold(strings.TrimSpace(earlier), strings.TrimSpace(o)) {
				return invalid("options", "варианты повторяются")
			}
		}
	}

	switch p.Kind {
	case KindSingle:
		if p.MaxChoices != 0 {
			return invalid("max_choices", "неприменимо к single")
		}
	case KindMultiple:
		if p.MaxChoices < 0 {
			return invalid("max_choices", "не может быть отрицательным")
		}
		if p.MaxChoices > len(p.Options) {
			return invalid("max_choices", "больше, чем вариантов")
		}
	}

	if p.StartsAt.IsZero() || p.EndsAt.IsZero() {
		return invalid("starts_at/ends_at", "обязательны")
	}
	if !p.EndsAt.After(p.StartsAt) {
		return invalid("ends_at", "должно быть позже starts_at")
	}
	return nil
}

const selectPollColumns = `id, question, kind, coalesce(max_choices, 0), starts_at, ends_at, generation`

func (s *Store) Create(ctx context.Context, p CreateParams) (*Poll, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("начало транзакции: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id := uuid.New()
	var maxChoices *int
	if p.Kind == KindMultiple && p.MaxChoices > 0 {
		maxChoices = &p.MaxChoices
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO polls (id, question, kind, max_choices, starts_at, ends_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, strings.TrimSpace(p.Question), string(p.Kind), maxChoices, p.StartsAt, p.EndsAt)
	if err != nil {
		return nil, fmt.Errorf("вставка опроса: %w", err)
	}

	// Опрос и варианты создаются одной транзакцией: опрос без вариантов —
	// состояние, в котором счётчики пусты, а голосовать формально можно.
	options := make([]Option, len(p.Options))
	batch := &pgx.Batch{}
	for i, text := range p.Options {
		options[i] = Option{ID: uuid.New(), Text: strings.TrimSpace(text), Position: i}
		batch.Queue(`INSERT INTO poll_options (id, poll_id, text, position) VALUES ($1, $2, $3, $4)`,
			options[i].ID, id, options[i].Text, i)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return nil, fmt.Errorf("вставка вариантов: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("коммит: %w", err)
	}

	return &Poll{
		ID: id, Question: strings.TrimSpace(p.Question), Kind: p.Kind,
		MaxChoices: p.MaxChoices, StartsAt: p.StartsAt, EndsAt: p.EndsAt,
		Generation: 1, Options: options,
	}, nil
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Poll, error) {
	var p Poll
	var kind string
	err := s.db.QueryRow(ctx, `SELECT `+selectPollColumns+` FROM polls WHERE id = $1`, id).
		Scan(&p.ID, &p.Question, &kind, &p.MaxChoices, &p.StartsAt, &p.EndsAt, &p.Generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("чтение опроса: %w", err)
	}
	p.Kind = Kind(kind)

	p.Options, err = s.options(ctx, []uuid.UUID{id})
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Active возвращает опросы, которые ещё не закончились: и armed, и open.
//
// Используется только прогревом. Armed включены намеренно — метаданные должны
// лечь в память ДО эфира, а не в первую его секунду (architecture.md §6).
func (s *Store) Active(ctx context.Context, now time.Time) ([]*Poll, error) {
	rows, err := s.db.Query(ctx, `SELECT `+selectPollColumns+`
		FROM polls WHERE ends_at > $1 ORDER BY starts_at`, now)
	if err != nil {
		return nil, fmt.Errorf("активные опросы: %w", err)
	}
	return s.scanPolls(ctx, rows)
}

func (s *Store) List(ctx context.Context, limit, offset int) ([]*Poll, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.Query(ctx, `SELECT `+selectPollColumns+`
		FROM polls ORDER BY created_at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("список опросов: %w", err)
	}
	return s.scanPolls(ctx, rows)
}

// scanPolls разбирает строки опросов и догружает их варианты одним запросом.
//
// Один запрос на все варианты, а не по запросу на опрос: и список, и прогрев
// редки, но N+1 в них всё равно нечем оправдать.
func (s *Store) scanPolls(ctx context.Context, rows pgx.Rows) ([]*Poll, error) {
	defer rows.Close()

	var (
		polls []*Poll
		ids   []uuid.UUID
	)
	for rows.Next() {
		var p Poll
		var kind string
		if err := rows.Scan(&p.ID, &p.Question, &kind, &p.MaxChoices, &p.StartsAt, &p.EndsAt, &p.Generation); err != nil {
			return nil, fmt.Errorf("разбор опроса: %w", err)
		}
		p.Kind = Kind(kind)
		polls = append(polls, &p)
		ids = append(ids, p.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение опросов: %w", err)
	}
	if len(polls) == 0 {
		return nil, nil
	}

	byPoll, err := s.optionsByPoll(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, p := range polls {
		p.Options = byPoll[p.ID]
	}
	return polls, nil
}

func (s *Store) options(ctx context.Context, ids []uuid.UUID) ([]Option, error) {
	byPoll, err := s.optionsByPoll(ctx, ids)
	if err != nil {
		return nil, err
	}
	return byPoll[ids[0]], nil
}

// optionsByPoll читает варианты сразу для нескольких опросов, упорядоченные по
// position: индекс в срезе должен совпадать с индексом в массиве счётчиков.
func (s *Store) optionsByPoll(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID][]Option, error) {
	rows, err := s.db.Query(ctx, `
		SELECT poll_id, id, text, position FROM poll_options
		WHERE poll_id = ANY($1) ORDER BY poll_id, position`, ids)
	if err != nil {
		return nil, fmt.Errorf("чтение вариантов: %w", err)
	}
	defer rows.Close()

	out := make(map[uuid.UUID][]Option, len(ids))
	for rows.Next() {
		var pollID uuid.UUID
		var o Option
		if err := rows.Scan(&pollID, &o.ID, &o.Text, &o.Position); err != nil {
			return nil, fmt.Errorf("разбор варианта: %w", err)
		}
		out[pollID] = append(out[pollID], o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("чтение вариантов: %w", err)
	}
	return out, nil
}

// Aggregate — агрегированный результат. Сырых голосов не существует, поэтому
// это единственная форма, в которой результат вообще доступен (architecture.md §7).
type Aggregate struct {
	// Voters — число проголосовавших, знаменатель для процентов. При
	// KindMultiple не равно сумме голосов (architecture.md §5.8).
	Voters int64
	Votes  map[uuid.UUID]int64
}

func (s *Store) Results(ctx context.Context, pollID uuid.UUID) (*Aggregate, error) {
	// TODO(шаг 6): чтение poll_results и poll_totals.
	return nil, errors.New("не реализовано: шаг 6")
}

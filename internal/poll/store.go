package poll

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

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

func (s *Store) Create(ctx context.Context, p CreateParams) (*Poll, error) {
	// TODO: одна транзакция на polls + poll_options.
	return nil, nil
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Poll, error) {
	// TODO
	return nil, nil
}

func (s *Store) List(ctx context.Context, limit, offset int) ([]*Poll, error) {
	// TODO
	return nil, nil
}

// Aggregate — агрегированный результат. Сырых голосов не существует, поэтому
// это единственная форма, в которой результат вообще доступен (architecture.md §7).
type Aggregate struct {
	Voters  int64 // число проголосовавших, знаменатель для процентов
	PerName map[uuid.UUID]int64
}

func (s *Store) Results(ctx context.Context, pollID uuid.UUID) (*Aggregate, error) {
	// TODO
	return nil, nil
}

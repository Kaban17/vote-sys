// Package results отвечает за чтение агрегатов из Redis и их перенос в Postgres.
package results

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/boar/vote-sys/internal/vote"
)

var errBadInt = errors.New("не число")

// Raw — сырые счётчики опроса, просуммированные по шардам.
type Raw struct {
	// Votes — по идентификатору варианта в строковом виде: именно так они
	// лежат полями хеша.
	Votes  map[string]int64
	Voters int64
}

// Source — чтение агрегатов. Интерфейс симметричен vote.Sink: пишущая и
// читающая стороны устроены одинаково, и обе проверяются без живого Redis.
type Source interface {
	Read(ctx context.Context, pollID uuid.UUID, shards int) (Raw, error)
}

// RedisSource собирает счётчики со всех шардов одним пайплайном.
//
// Читающее зеркало батчера: инстанс раз в секунду забирает shards хешей и
// shards счётчиков участников, а не ходит в Redis на каждый запрос к
// результатам. Redis не видит ни 250K записей, ни 250K чтений
// (architecture.md §5.2).
type RedisSource struct {
	rdb     redis.UniversalClient
	timeout time.Duration
}

func NewRedisSource(rdb redis.UniversalClient, timeout time.Duration) *RedisSource {
	return &RedisSource{rdb: rdb, timeout: timeout}
}

func (s *RedisSource) Read(ctx context.Context, pollID uuid.UUID, shards int) (Raw, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	pipe := s.rdb.Pipeline()
	counts := make([]*redis.MapStringStringCmd, shards)
	voters := make([]*redis.StringCmd, shards)
	for i := 0; i < shards; i++ {
		counts[i] = pipe.HGetAll(ctx, vote.CountsKey(pollID, i))
		voters[i] = pipe.Get(ctx, vote.VotersKey(pollID, i))
	}

	// Exec возвращает redis.Nil, если хоть одна команда не нашла ключ. Для нас
	// это норма: шард, в который ещё не голосовали, ключа не имеет.
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return Raw{}, err
	}

	out := Raw{Votes: make(map[string]int64)}
	for i := 0; i < shards; i++ {
		m, err := counts[i].Result()
		if err != nil && err != redis.Nil {
			return Raw{}, err
		}
		for optID, raw := range m {
			n, err := parseInt(raw)
			if err != nil {
				continue // мусор в хеше не должен ронять чтение всего опроса
			}
			out.Votes[optID] += n
		}

		v, err := voters[i].Int64()
		if err != nil && err != redis.Nil {
			return Raw{}, err
		}
		out.Voters += v
	}
	return out, nil
}

func parseInt(s string) (int64, error) {
	var n int64
	var neg bool
	for i, c := range s {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			return 0, errBadInt
		}
		n = n*10 + int64(c-'0')
	}
	if neg {
		n = -n
	}
	return n, nil
}

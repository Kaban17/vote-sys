package vote

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// countersTTL — срок жизни ключей счётчиков в Redis.
//
// Redis здесь точка синхронизации, а не хранилище: долговременные агрегаты
// живут в Postgres. Срок с большим запасом относительно эфира — чтобы
// восстановление после сбоя не упёрлось в исчезнувшие ключи, но и чтобы память
// не росла бесконечно.
const countersTTL = 7 * 24 * time.Hour

// Sink — приёмник дельт.
//
// Батчер отвечает за то, КОГДА сбрасывать и как пережить неудачу; куда именно
// уходят числа, его не касается. Разделение то же, что у dedup.Checker, и
// оно же делает батчер проверяемым без живого Redis.
type Sink interface {
	Push(ctx context.Context, pollID uuid.UUID, shard int, optionIDs []string, d Delta) error
}

// RedisSink отправляет дельту одним пайплайном:
//
//	HINCRBY poll:{id}:counts:{shard} {option_id} {delta}   × изменившиеся опции
//	INCRBY  poll:{id}:voters:{shard} {delta}
//	EXPIRE  на оба ключа
type RedisSink struct {
	rdb     redis.UniversalClient
	timeout time.Duration
}

func NewRedisSink(rdb redis.UniversalClient, timeout time.Duration) *RedisSink {
	return &RedisSink{rdb: rdb, timeout: timeout}
}

func (s *RedisSink) Push(ctx context.Context, pollID uuid.UUID, shard int, optionIDs []string, d Delta) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	countsKey := CountsKey(pollID, shard)
	votersKey := VotersKey(pollID, shard)

	pipe := s.rdb.Pipeline()
	for i, n := range d.Options {
		if n == 0 {
			continue
		}
		pipe.HIncrBy(ctx, countsKey, optionIDs[i], n)
	}
	if d.Voters != 0 {
		pipe.IncrBy(ctx, votersKey, d.Voters)
	}
	pipe.Expire(ctx, countsKey, countersTTL)
	pipe.Expire(ctx, votersKey, countersTTL)

	_, err := pipe.Exec(ctx)
	return err
}

// CountsKey и VotersKey — единственное место, где задаётся форма ключей.
// Читающая сторона (снапшоттер, шаг 6) обязана пользоваться ими же.
func CountsKey(pollID uuid.UUID, shard int) string {
	return fmt.Sprintf("poll:%s:counts:%d", pollID, shard)
}

func VotersKey(pollID uuid.UUID, shard int) string {
	return fmt.Sprintf("poll:%s:voters:%d", pollID, shard)
}

package dedup

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/boar/vote-sys/internal/breaker"
)

// RedisChecker — реализация поверх SET NX EX.
//
// SET NX атомарен без блокировок за счёт однопоточности шарда Redis. TTL
// встроен, поэтому уборка 10 млн ключей не требует отдельного процесса.
type RedisChecker struct {
	rdb     redis.UniversalClient
	timeout time.Duration // DEDUP_TIMEOUT, ограничивает урон от одного запроса
	br      *breaker.Breaker
}

func NewRedisChecker(rdb redis.UniversalClient, timeout time.Duration, br *breaker.Breaker) *RedisChecker {
	return &RedisChecker{rdb: rdb, timeout: timeout, br: br}
}

func (c *RedisChecker) Check(ctx context.Context, pollID uuid.UUID, token string, ttl time.Duration) Result {
	// TODO:
	//   1. br.Allow() == false → Bypassed, БЕЗ обращения к Redis.
	//      Это и есть смысл breaker'а: при деградации (Redis жив, но отвечает
	//      за 200 мс) каждый запрос иначе честно выждет свой таймаут, и при
	//      250K RPS это мгновенно выест пул соединений (architecture.md §6).
	//   2. ctx с таймаутом c.timeout.
	//   3. SET key "1" NX EX ttl.
	//   4. ошибка/таймаут → br.Failure(); Bypassed.
	//      успех → br.Success(); First или Duplicate по значению.
	return Bypassed
}

// key — dedup:{poll_id}:{token}.
func key(pollID uuid.UUID, token string) string {
	// TODO
	return ""
}

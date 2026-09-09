package dedup

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/boar/vote-sys/internal/breaker"
)

// Store — единственная операция, которая нужна дедупу.
//
// Узкий интерфейс вместо redis.UniversalClient: он делает проверяемым и сам
// дедуп, и — что важнее — поведение при отказе, которое иначе пришлось бы
// воспроизводить живым Redis.
type Store interface {
	// SetNX возвращает true, если ключ установлен впервые.
	SetNX(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

// RedisStore — SET NX EX поверх go-redis.
//
// SET NX атомарен без блокировок за счёт однопоточности шарда Redis. TTL
// встроен, поэтому уборка 10 млн ключей не требует отдельного процесса.
type RedisStore struct {
	rdb redis.UniversalClient
}

func NewRedisStore(rdb redis.UniversalClient) *RedisStore {
	return &RedisStore{rdb: rdb}
}

func (s *RedisStore) SetNX(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return s.rdb.SetNX(ctx, key, 1, ttl).Result()
}

// GuardedChecker — дедуп с таймаутом и circuit breaker'ом.
type GuardedChecker struct {
	store   Store
	timeout time.Duration // DEDUP_TIMEOUT, ограничивает урон от одного запроса
	br      *breaker.Breaker
}

func NewGuardedChecker(store Store, timeout time.Duration, br *breaker.Breaker) *GuardedChecker {
	return &GuardedChecker{store: store, timeout: timeout, br: br}
}

func (c *GuardedChecker) Check(ctx context.Context, pollID uuid.UUID, voterID string, ttl time.Duration) Result {
	// Разомкнутая цепь означает, что обращения не будет ВООБЩЕ. В этом весь
	// смысл breaker'а: при деградации Redis (жив, но отвечает за 200 мс) каждый
	// запрос иначе честно выждал бы свой таймаут, и при 250K RPS это мгновенно
	// выело бы пул соединений (architecture.md §6).
	if !c.br.Allow() {
		return Bypassed
	}

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	first, err := c.withDeadline(ctx, Key(pollID, voterID), ttl)
	if err != nil {
		c.br.Failure()
		return Bypassed
	}
	c.br.Success()

	if first {
		return First
	}
	return Duplicate
}

// withDeadline гарантирует, что вызов вернётся не позже дедлайна контекста.
//
// Передать контекст в клиент недостаточно — это выяснилось замером на стенде.
// Когда имя Redis перестаёт резолвиться, первая партия запросов висела до 10
// секунд при бюджете 50 мс: контекст соблюдался на дозвоне, но не на ожидании
// в пуле соединений клиента. Гарантия «таймаут ограничивает урон от одного
// запроса» (architecture.md §6) держалась только на breaker'е, а он по
// построению срабатывает лишь после порога ошибок — то есть первую партию не
// защищает.
//
// Сторож стоит горутину и канал на вызов: сотни наносекунд против
// миллисекундного round-trip'а, доли процента. Зависшая горутина завершится
// сама, когда клиент наконец вернётся: канал буферизован, отправка не
// блокируется.
func (c *GuardedChecker) withDeadline(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	type result struct {
		first bool
		err   error
	}
	done := make(chan result, 1)

	go func() {
		first, err := c.store.SetNX(ctx, key, ttl)
		done <- result{first, err}
	}()

	select {
	case r := <-done:
		return r.first, r.err
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// Key — dedup:{poll_id}:{voter_id}.
func Key(pollID uuid.UUID, voterID string) string {
	return fmt.Sprintf("dedup:%s:%s", pollID, voterID)
}

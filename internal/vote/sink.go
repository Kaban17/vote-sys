package vote

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
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

// ErrMaybeApplied — отправка не подтверждена, но команды могли выполниться.
//
// Различие принципиально для восстановления. HINCRBY не идемпотентен: повтор
// дельты, которая на самом деле применилась, задваивает голоса. А двойной счёт
// для опроса хуже потери — на этом же основании дедуп стоит до инкремента
// (architecture.md §5.4). Поэтому при неоднозначном исходе батч выбрасывается,
// и только при заведомо недоставленной отправке возвращается в счётчики.
var ErrMaybeApplied = errors.New("отправка не подтверждена, дельта могла примениться")

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

	if _, err := pipe.Exec(ctx); err != nil {
		return classify(err)
	}
	return nil
}

// classify отличает «точно не доставлено» от «может быть, уже применилось».
//
// Таймаут и обрыв на чтении означают, что команды успели уйти и Redis их
// выполнил — просто ответ не дошёл. Ошибка дозвона означает, что не ушло
// ничего. Первое неоднозначно, второе безопасно повторить.
func classify(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return fmt.Errorf("%w: %v", ErrMaybeApplied, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%w: %v", ErrMaybeApplied, err)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op != "dial" {
		// read/write по установленному соединению: часть команд уже могла уйти.
		return fmt.Errorf("%w: %v", ErrMaybeApplied, err)
	}
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

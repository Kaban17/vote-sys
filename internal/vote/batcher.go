package vote

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Batcher сводит импульс к постоянной нагрузке: 250K инкрементов в секунду в
// памяти превращаются в ~1000 ops/sec в Redis (architecture.md §2).
//
// Именно батчинг делает счётчики не узким местом — вопреки интуиции, которая
// подсказывает бояться hot key. Реальный дефицит в дедупе, который батчить
// нельзя.
type Batcher struct {
	rdb    redis.UniversalClient
	shard  int           // ординал инстанса % COUNTER_SHARDS (architecture.md §7)
	every  time.Duration // FLUSH_INTERVAL
	thresh int64         // FLUSH_THRESHOLD: flush по таймеру ИЛИ по порогу — что раньше

	polls map[uuid.UUID]*Counters
}

func NewBatcher(rdb redis.UniversalClient, shard int, every time.Duration, thresh int64) *Batcher {
	// TODO
	return nil
}

// Register заводит счётчики опроса. Вызывается при переходе опроса в armed,
// до эфира (architecture.md §6).
func (b *Batcher) Register(p uuid.UUID, numOptions int) *Counters {
	// TODO
	return nil
}

// Run крутит цикл сброса до отмены контекста, затем делает финальный flush.
//
// Финальный flush при SIGTERM — то, что превращает плановый рестарт в нулевую
// потерю. Оценка в ~1600 потерянных голосов относится только к жёсткому отказу
// ноды (architecture.md §5.4).
func (b *Batcher) Run(ctx context.Context) error {
	// TODO:
	//   for {
	//       select {
	//       case <-ticker.C:  b.flush(ctx)
	//       case <-ctx.Done(): b.flush(context.WithoutCancel(ctx)); return nil
	//       }
	//   }
	return nil
}

// flush отправляет накопленное одним пайплайном:
//
//	HINCRBY poll:{id}:counts:{shard}  {option_id} {delta}   × число опций
//	INCRBY  poll:{id}:voters:{shard}  {delta}
//
// При ошибке значения возвращаются через Counters.Restore и повторяются на
// следующем тике.
func (b *Batcher) flush(ctx context.Context) {
	// TODO
}

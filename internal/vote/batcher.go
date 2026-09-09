package vote

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// entry — счётчики опроса вместе с именами полей для HINCRBY.
type entry struct {
	counters  *Counters
	optionIDs []string // по индексам опций
}

// Batcher сводит импульс к постоянной нагрузке: 250K инкрементов в секунду в
// памяти превращаются в ~1000 ops/sec в Redis (architecture.md §2).
//
// Именно батчинг делает счётчики не узким местом — вопреки интуиции, которая
// подсказывает бояться hot key. Реальный дефицит в дедупе, который батчить
// нельзя.
//
// Порога на размер батча нет намеренно. Он был в первоначальной спецификации с
// обоснованием «иначе батч распухнет за интервал», но для агрегации счётчиков
// это неверно: за 150 мс уходит одинаковое число команд и при 10 RPS, и при
// 250K — растёт только значение дельты, а HINCRBY от него не дорожает. Порог не
// покупал бы ничего, зато требовал бы общего счётчика записей, то есть одной
// горячей кэш-линии на весь инстанс.
type Batcher struct {
	sink  Sink
	shard int           // ординал инстанса % COUNTER_SHARDS (architecture.md §7)
	every time.Duration // FLUSH_INTERVAL
	log   *slog.Logger

	// polls читается на каждом голосе и меняется раз на опрос, поэтому
	// copy-on-write: читатели идут без блокировок, писатели сериализуются
	// мьютексом между собой.
	polls atomic.Pointer[map[uuid.UUID]*entry]
	mu    sync.Mutex
}

func NewBatcher(sink Sink, shard int, every time.Duration, log *slog.Logger) *Batcher {
	b := &Batcher{sink: sink, shard: shard, every: every, log: log}
	empty := make(map[uuid.UUID]*entry)
	b.polls.Store(&empty)
	return b
}

// Lookup — быстрый путь: чтение атомарного указателя без блокировок.
//
// Отделён от Register намеренно. Register принимает имена полей для HINCRBY, а
// собирать их пришлось бы на каждый голос — то есть выделять память на горячем
// пути ради данных, которые нужны один раз за всё время жизни опроса.
func (b *Batcher) Lookup(pollID uuid.UUID) (*Counters, bool) {
	if e, ok := (*b.polls.Load())[pollID]; ok {
		return e.counters, true
	}
	return nil, false
}

// Register заводит счётчики опроса. Идемпотентен: при повторном вызове вернёт
// уже существующие.
func (b *Batcher) Register(pollID uuid.UUID, optionIDs []string) *Counters {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Повторная проверка под мьютексом: между быстрым путём и захватом замка
	// опрос мог зарегистрировать кто-то другой.
	cur := *b.polls.Load()
	if e, ok := cur[pollID]; ok {
		return e.counters
	}

	next := make(map[uuid.UUID]*entry, len(cur)+1)
	for k, v := range cur {
		next[k] = v
	}
	e := &entry{
		counters:  NewCounters(len(optionIDs)),
		optionIDs: optionIDs,
	}
	next[pollID] = e
	b.polls.Store(&next)
	return e.counters
}

// Run крутит цикл сброса до отмены контекста, затем делает финальный flush.
//
// Финальный flush при SIGTERM — то, что превращает плановый рестарт в нулевую
// потерю. Оценка в ~1600 потерянных голосов относится только к жёсткому отказу
// ноды (architecture.md §5.4).
func (b *Batcher) Run(ctx context.Context) {
	t := time.NewTicker(b.every)
	defer t.Stop()

	for {
		select {
		case <-t.C:
			b.Flush(ctx)
		case <-ctx.Done():
			b.Flush(context.WithoutCancel(ctx))
			return
		}
	}
}

// Flush отправляет накопленное по всем опросам.
func (b *Batcher) Flush(ctx context.Context) {
	for id, e := range *b.polls.Load() {
		d := e.counters.Swap()
		if d.IsEmpty() {
			continue
		}
		if err := b.sink.Push(ctx, id, b.shard, e.optionIDs, d); err != nil {
			// Swap уже забрал значения из памяти: без возврата они потерялись бы
			// при полностью живом инстансе, просто из-за сетевой ошибки.
			e.counters.Restore(d)
			b.log.Error("не удалось сбросить счётчики", "poll_id", id, "err", err)
		}
	}
}

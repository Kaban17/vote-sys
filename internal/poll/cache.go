package poll

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

const (
	// negativeTTL — сколько помнить, что опроса нет.
	//
	// Без отрицательного кэша поток голосов на несуществующий poll_id бил бы в
	// Postgres на каждом запросе. При 250K RPS это уронило бы базу, которая по
	// замыслу горячего пути не касается вовсе (architecture.md §2). Срок
	// короткий: опрос могли создать секунду назад.
	negativeTTL = 10 * time.Second

	// maxNegative — потолок отрицательного кэша.
	//
	// Сам кэш промахов — тоже вектор: генератор случайных uuid раздул бы карту
	// без границы. По достижении потолка кэш сбрасывается целиком: грубо, зато
	// O(1) и без отдельного вытеснителя.
	maxNegative = 4096
)

// Cache — процессный кэш метаданных опроса.
//
// Опрос неизменяем, поэтому положительные записи живут весь срок его жизни без
// инвалидации. singleflight защищает от stampede при одновременном старте
// инстансов: максимум один запрос на инстанс на опрос, то есть 100 стартующих
// подов дают 100 запросов за одну строку, а не шторм (architecture.md §6).
type Cache struct {
	store *Store
	group singleflight.Group

	mu       sync.RWMutex
	byID     map[uuid.UUID]*Poll
	missedAt map[uuid.UUID]time.Time
}

func NewCache(store *Store) *Cache {
	return &Cache{
		store:    store,
		byID:     make(map[uuid.UUID]*Poll),
		missedAt: make(map[uuid.UUID]time.Time),
	}
}

func (c *Cache) lookup(id uuid.UUID, now time.Time) (*Poll, bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if p, ok := c.byID[id]; ok {
		return p, true, false
	}
	if at, ok := c.missedAt[id]; ok && now.Sub(at) < negativeTTL {
		return nil, false, true
	}
	return nil, false, false
}

// Get возвращает опрос из памяти, при промахе — читает из Postgres.
func (c *Cache) Get(ctx context.Context, id uuid.UUID) (*Poll, error) {
	now := time.Now()
	if p, hit, negative := c.lookup(id, now); hit {
		return p, nil
	} else if negative {
		return nil, ErrNotFound
	}

	v, err, _ := c.group.Do(id.String(), func() (any, error) {
		p, err := c.store.Get(ctx, id)
		if err != nil {
			if err == ErrNotFound {
				c.rememberMiss(id)
			}
			return nil, err
		}
		c.Put(p)
		return p, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*Poll), nil
}

// Put кладёт опрос в кэш. Публичный, потому что прогрев наполняет кэш пачкой.
func (c *Cache) Put(p *Poll) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byID[p.ID] = p
	delete(c.missedAt, p.ID)
}

func (c *Cache) rememberMiss(id uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.missedAt) >= maxNegative {
		c.missedAt = make(map[uuid.UUID]time.Time, maxNegative)
	}
	c.missedAt[id] = time.Now()
}

// Warm загружает опросы, которые ещё не закончились, до начала приёма трафика.
//
// Инстанс обязан выйти на пик прогретым: реагировать во время
// шестидесятисекундного события невозможно, и загрузка метаданных в первую
// секунду эфира — это ровно та задержка, которой нельзя допустить
// (architecture.md §1, §6).
func (c *Cache) Warm(ctx context.Context) (int, error) {
	polls, err := c.store.Active(ctx, time.Now())
	if err != nil {
		return 0, err
	}
	for _, p := range polls {
		c.Put(p)
	}
	return len(polls), nil
}

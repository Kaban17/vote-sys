package poll

import (
	"context"
	"sync"

	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

// Cache — процессный кэш метаданных опроса.
//
// Опрос неизменяем, поэтому кэш живёт весь срок его жизни без инвалидации.
// singleflight защищает от stampede при одновременном старте инстансов: максимум
// один запрос на инстанс на опрос, то есть 100 стартующих подов дают 100 запросов
// за одну строку, а не шторм (architecture.md §6).
type Cache struct {
	store *Store
	group singleflight.Group

	mu   sync.RWMutex
	byID map[uuid.UUID]*Poll
}

func NewCache(store *Store) *Cache {
	return &Cache{store: store, byID: make(map[uuid.UUID]*Poll)}
}

func (c *Cache) Get(ctx context.Context, id uuid.UUID) (*Poll, error) {
	// TODO: RLock → hit; иначе singleflight.Do вокруг store.Get.
	return nil, nil
}

// Warm загружает опросы в состоянии armed и open до начала эфира. Вызывается при
// старте инстанса: система должна выйти на пик уже прогретой, реагировать во
// время события невозможно (architecture.md §1, §6).
func (c *Cache) Warm(ctx context.Context) error {
	// TODO
	return nil
}

package platform

import (
	"context"

	"github.com/redis/go-redis/v9"
)

// NewRedis поднимает клиент с предсозданным пулом.
//
// Размер пула — параметр горячего пути: на каждый голос приходится один
// синхронный round-trip, и нехватка соединений выражается в очереди на пуле,
// то есть в латентности (architecture.md §2).
func NewRedis(ctx context.Context, addr string, poolSize int) (redis.UniversalClient, error) {
	// TODO: redis.NewClient с PoolSize и MinIdleConns = PoolSize;
	// затем Ping для прогрева соединений.
	return nil, nil
}

package platform

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
)

// NewRedis поднимает клиент с предсозданным пулом.
//
// Размер пула — параметр горячего пути: на каждый голос приходится один
// синхронный round-trip, и нехватка соединений выражается в очереди на пуле,
// то есть в латентности (architecture.md §2).
func NewRedis(ctx context.Context, addr string, poolSize int) (redis.UniversalClient, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		PoolSize: poolSize,
		// Пул держится полным: см. комментарий к MinConns в postgres.go.
		MinIdleConns: poolSize,
	})

	if err := warmRedis(ctx, rdb, poolSize); err != nil {
		_ = rdb.Close()
		return nil, err
	}
	return rdb, nil
}

// warmRedis открывает соединения принудительно.
//
// MinIdleConns, как и MinConns у pgxpool, наполняет пул в фоне. Параллельные
// Ping'и занимают соединения одновременно, поэтому клиент вынужден открыть их
// все, а не переиспользовать одно.
func warmRedis(ctx context.Context, rdb redis.UniversalClient, n int) error {
	var g errgroup.Group
	for i := 0; i < n; i++ {
		g.Go(func() error {
			return rdb.Ping(ctx).Err()
		})
	}
	if err := g.Wait(); err != nil {
		return fmt.Errorf("прогрев пула redis: %w", err)
	}
	return nil
}

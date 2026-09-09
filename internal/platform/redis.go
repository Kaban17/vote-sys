package platform

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"
)

// NewRedis поднимает клиент с предсозданным пулом.
//
// Размер пула — параметр горячего пути: на каждый голос приходится один
// синхронный round-trip, и нехватка соединений выражается в очереди на пуле,
// то есть в латентности (architecture.md §2).
// NewRedis поднимает клиент с предсозданным пулом и жёсткими таймаутами.
//
// budget — предельное время одного обращения (DEDUP_TIMEOUT).
//
// Полагаться на context.WithTimeout в вызывающем коде НЕДОСТАТОЧНО, и это
// выяснилось на стенде: probe circuit breaker'а к остановленному Redis висел
// 16,7 секунды при бюджете в 50 мс. Клиент по умолчанию делает собственные
// ретраи с backoff и имеет свои таймауты, которые контекст вызова не отменяет.
// Ограничения ниже задают тот же бюджет там, где он действительно соблюдается.
func NewRedis(ctx context.Context, addr string, poolSize int, budget time.Duration) (redis.UniversalClient, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		PoolSize: poolSize,
		// Пул держится полным: см. комментарий к MinConns в postgres.go.
		MinIdleConns: poolSize,

		// Ретраи выключены намеренно. Повторять обращение к неотвечающему Redis
		// на горячем пути — ровно то, чего делать нельзя: это умножает задержку
		// вместо того, чтобы её ограничить. Восстановлением занимается breaker,
		// а неудача стоит дёшево, потому что политика — fail-open
		// (architecture.md §6).
		MaxRetries: -1,

		DialTimeout:  budget,
		ReadTimeout:  budget,
		WriteTimeout: budget,
		PoolTimeout:  budget,

		Dialer: boundedDialer(budget),
	})

	if err := warmRedis(ctx, rdb, poolSize); err != nil {
		_ = rdb.Close()
		return nil, err
	}
	return rdb, nil
}

// boundedDialer ограничивает бюджетом ВЕСЬ дозвон, включая разрешение имени.
//
// Одного DialTimeout недостаточно, и это измерено на стенде. Два сценария отказа
// ведут себя по-разному:
//
//   - Redis не отвечает, имя резолвится (`docker compose pause`) — худший запрос
//     53 мс, то есть бюджет соблюдён;
//   - имя Redis исчезло вовсе (`docker compose stop`) — до 10 секунд, потому что
//     ожидание задаёт resolv.conf своими timeout и attempts, а не наш таймаут.
//
// Второй случай в проде реже (обычно приходит connection refused), но именно он
// опаснее: при 250K RPS десятисекундные запросы — это и есть выеденный пул.
// Явный контекст с дедлайном закрывает оба.
func boundedDialer(budget time.Duration) func(context.Context, string, string) (net.Conn, error) {
	d := &net.Dialer{Timeout: budget, KeepAlive: 5 * time.Minute}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		ctx, cancel := context.WithTimeout(ctx, budget)
		defer cancel()
		return d.DialContext(ctx, network, addr)
	}
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

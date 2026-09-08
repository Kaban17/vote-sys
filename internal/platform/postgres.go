// Package platform — подключения к внешним системам.
//
// Общее требование ко всему пакету: пулы создаются и ПРОГРЕВАЮТСЯ при старте,
// а не лениво по первому запросу. Иначе первые секунды пика уйдут на установку
// сотен TCP-соединений одновременно — а в шестидесятисекундном окне это заметная
// доля всего события (architecture.md §6).
package platform

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPostgres поднимает пул и возвращает его уже прогретым.
func NewPostgres(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("разбор POSTGRES_DSN: %w", err)
	}

	// MinConns = MaxConns: пул держится полным всё время жизни инстанса.
	// Нагрузка импульсная, и «экономия» на простое обернулась бы установкой
	// соединений ровно в тот момент, когда их устанавливать некогда.
	cfg.MaxConns = maxConns
	cfg.MinConns = maxConns

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("создание пула postgres: %w", err)
	}

	if err := warmPool(ctx, pool, int(maxConns)); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// warmPool заставляет пул физически открыть соединения.
//
// MinConns сам по себе гарантии не даёт: pgxpool доводит пул до минимума
// фоновой горутиной, то есть когда-нибудь. Одновременный захват n соединений
// открывает их здесь и сейчас, до того как инстанс объявит себя готовым.
func warmPool(ctx context.Context, pool *pgxpool.Pool, n int) error {
	conns := make([]*pgxpool.Conn, 0, n)
	defer func() {
		for _, c := range conns {
			c.Release()
		}
	}()

	for i := 0; i < n; i++ {
		c, err := pool.Acquire(ctx)
		if err != nil {
			return fmt.Errorf("прогрев пула postgres (соединение %d из %d): %w", i+1, n, err)
		}
		conns = append(conns, c)
	}

	// Соединение установлено, но проверить хочется и то, что база отвечает.
	if err := conns[0].Ping(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	return nil
}

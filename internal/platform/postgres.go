// Package platform — подключения к внешним системам.
//
// Общее требование ко всему пакету: пулы создаются и ПРОГРЕВАЮТСЯ при старте,
// а не лениво по первому запросу. Иначе первые секунды пика уйдут на установку
// сотен TCP-соединений одновременно — а в шестидесятисекундном окне это заметная
// доля всего события (architecture.md §6).
package platform

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

func NewPostgres(ctx context.Context, dsn string, maxConns int32) (*pgxpool.Pool, error) {
	// TODO: pgxpool.ParseConfig; MinConns = MaxConns, чтобы пул был полным
	// сразу; pool.Ping для проверки.
	return nil, nil
}

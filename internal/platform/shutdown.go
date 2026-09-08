package platform

import (
	"context"
	"net/http"
	"time"
)

// GracefulShutdown останавливает приём и даёт фоновым задачам доработать.
//
// Это то, что превращает плановый рестарт в НУЛЕВУЮ потерю голосов: батчер
// успевает сделать финальный flush. Оценка в ~1600 потерянных голосов относится
// только к жёсткому отказу ноды (architecture.md §5.4).
//
// Порядок важен:
//  1. перестать принимать новые запросы (http.Server.Shutdown);
//  2. дождаться завершения текущих;
//  3. финальный flush батчера;
//  4. закрыть пулы.
func GracefulShutdown(ctx context.Context, srv *http.Server, timeout time.Duration, tasks ...func(context.Context) error) error {
	// TODO
	return nil
}

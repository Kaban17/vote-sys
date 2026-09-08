package platform

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// GracefulShutdown останавливает приём и даёт фоновым задачам доработать.
//
// Это то, что превращает плановый рестарт в НУЛЕВУЮ потерю голосов: батчер
// успевает сделать финальный flush. Оценка в ~1600 потерянных голосов относится
// только к жёсткому отказу ноды (architecture.md §5.4).
//
// Порядок важен: сначала перестать принимать новые запросы и дождаться текущих,
// и только потом сбрасывать батч. Обратный порядок сбросил бы счётчики, которые
// тут же пополнят ещё не завершённые хендлеры.
func GracefulShutdown(ctx context.Context, srv *http.Server, timeout time.Duration, tasks ...func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	var errs []error

	// 1-2. Закрыть приём, дождаться завершения текущих запросов.
	if err := srv.Shutdown(ctx); err != nil {
		errs = append(errs, fmt.Errorf("остановка http-сервера: %w", err))
	}

	// 3. Финальный flush и прочие завершающие задачи.
	//
	// Выполняются даже если Shutdown вернул ошибку по таймауту: несброшенный
	// батч — это потерянные голоса, и попытаться стоит в любом случае.
	for _, task := range tasks {
		if err := task(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

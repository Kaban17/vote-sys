// Команда api — сервис голосований.
//
// Порядок запуска подчинён одному требованию: реагировать во время события
// невозможно, поэтому инстанс обязан выйти на пик уже прогретым
// (architecture.md §1, §6).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/boar/vote-sys/internal/config"
	"github.com/boar/vote-sys/internal/httpapi"
	"github.com/boar/vote-sys/internal/platform"
)

func main() {
	// Уровень задаётся до чтения конфигурации: ошибки самой конфигурации тоже
	// должны попадать в лог.
	levelVar := new(slog.LevelVar)
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: levelVar})))

	if err := run(levelVar); err != nil {
		slog.Error("остановка с ошибкой", "err", err)
		os.Exit(1)
	}
}

func run(levelVar *slog.LevelVar) error {
	// Сигнал прерывает прогрев тоже: под оркестратором инстанс могут снять и до
	// того, как он встал в строй.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	level, err := config.ParseLogLevel(cfg.LogLevel)
	if err != nil {
		return err
	}
	levelVar.Set(level)

	// 1. Пулы — предсозданные и прогретые, не лениво по первому запросу.
	db, err := platform.NewPostgres(ctx, cfg.PostgresDSN, cfg.PostgresMaxConns)
	if err != nil {
		return err
	}
	defer db.Close()

	rdb, err := platform.NewRedis(ctx, cfg.RedisAddr, cfg.RedisPoolSize)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	slog.Info("пулы прогреты",
		"postgres_conns", cfg.PostgresMaxConns,
		"redis_conns", cfg.RedisPoolSize,
		"shard", cfg.Shard(),
	)

	// TODO(шаг 2): poll.Cache.Warm — метаданные armed-опросов в память ДО эфира.
	// TODO(шаги 4-6): batcher.Run, snapshotter.Run, persister.Run.

	srv := httpapi.NewServer(cfg)

	// Агрегат трафика по ручкам. Заменяет построчный лог на горячем пути:
	// одна строка на ручку за интервал вместо сотен тысяч
	// (internal/httpapi/logstats.go).
	statsCtx, stopStats := context.WithCancel(context.Background())
	defer stopStats()
	go srv.RunStats(statsCtx, cfg.LogSummaryInterval)

	httpSrv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: srv.Routes(),

		// Таймауты рассчитаны на профиль события, а не на «разумные значения».
		//
		// ReadHeaderTimeout защищает от медленных клиентов, которых при 100 млн
		// зрителей будет много просто по статистике. WriteTimeout щедрее
		// длительности любого хендлера: голос обрабатывается за миллисекунды,
		// а всё, что дольше, — это уже деградация, у которой есть свои таймауты.
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      5 * time.Second,

		// Keep-alive от nginx: соединения переиспользуются, а не открываются
		// заново на каждый голос.
		IdleTimeout: 120 * time.Second,
	}

	// 2. Только теперь инстанс объявляет себя готовым и nginx начинает слать
	//    трафик.
	srv.MarkReady()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("приём запросов", "addr", cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	// 3. Снять готовность до остановки: nginx перестанет слать трафик раньше,
	//    чем сервер начнёт отказывать.
	srv.MarkUnready()
	slog.Info("остановка", "timeout", cfg.ShutdownTimeout)

	// TODO(шаг 4): передать сюда batcher.Flush — финальный сброс батча, то, что
	// превращает плановый рестарт в нулевую потерю голосов (architecture.md §5.4).
	err = platform.GracefulShutdown(ctx, httpSrv, cfg.ShutdownTimeout,
		func(context.Context) error {
			// Последний интервал почти наверняка не закрыт, а он и самый
			// интересный: в нём остановка.
			srv.FlushStats()
			return nil
		},
	)
	stopStats()
	return err
}

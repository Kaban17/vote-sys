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

	"github.com/boar/vote-sys/internal/breaker"
	"github.com/boar/vote-sys/internal/config"
	"github.com/boar/vote-sys/internal/dedup"
	"github.com/boar/vote-sys/internal/httpapi"
	"github.com/boar/vote-sys/internal/platform"
	"github.com/boar/vote-sys/internal/poll"
	"github.com/boar/vote-sys/internal/results"
	"github.com/boar/vote-sys/internal/token"
	"github.com/boar/vote-sys/internal/vote"
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

// resultsSettleWindow — сколько после конца опроса ещё собирать агрегаты.
//
// За это время долетают последние батчи со всех инстансов. Значение должно быть
// не меньше того, что использует публичный эндпоинт результатов при выборе
// заголовков кэширования.
const resultsSettleWindow = 2 * time.Minute

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

	// Два клиента, а не один, потому что у операций разные бюджеты.
	//
	// Дедуп лежит на горячем пути и обязан уложиться в 50 мс. Сброс счётчиков
	// идёт раз в 150 мс пайплайном и столько времени не имеет. Общий клиент
	// накрывал бы обе операции одним ReadTimeout — и на стенде это привело к
	// тому, что таймаут дедупа срабатывал на пайплайне батчера: команды
	// выполнялись, ответ не доходил, дельта возвращалась и слалась повторно,
	// задваивая голоса.
	rdb, err := platform.NewRedis(ctx, cfg.RedisAddr, cfg.RedisPoolSize, cfg.DedupTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	// Клиент счётчиков: бюджет с запасом относительно интервала сброса. Пул
	// маленький — писатель один, читатель снапшотов один.
	countersRdb, err := platform.NewRedis(ctx, cfg.RedisAddr, 8, 4*cfg.FlushInterval)
	if err != nil {
		return err
	}
	defer func() { _ = countersRdb.Close() }()

	slog.Info("пулы прогреты",
		"postgres_conns", cfg.PostgresMaxConns,
		"redis_conns", cfg.RedisPoolSize,
		"shard", cfg.Shard(),
	)

	// 2. Метаданные ещё не закончившихся опросов — в память ДО эфира.
	store := poll.NewStore(db)
	cache := poll.NewCache(store)
	warmed, err := cache.Warm(ctx)
	if err != nil {
		return err
	}
	slog.Info("кэш опросов прогрет", "polls", warmed)

	// 3. Батчер: 250K инкрементов в секунду в памяти → ~1000 ops/sec в Redis.
	sink := vote.NewRedisSink(countersRdb, 2*cfg.FlushInterval)
	batcher := vote.NewBatcher(sink, cfg.Shard(), cfg.FlushInterval, slog.Default())
	batcherCtx, stopBatcher := context.WithCancel(context.Background())
	// Страховка на путях раннего выхода: штатная остановка идёт из
	// GracefulShutdown, но контекст не должен утечь, если сервер не поднялся.
	defer stopBatcher()
	batcherDone := make(chan struct{})
	go func() {
		defer close(batcherDone)
		batcher.Run(batcherCtx)
	}()

	// 4. Дедуп: единственный синхронный round-trip на горячем пути и потолок
	//    всей системы (architecture.md §2). Таймаут ограничивает урон от одного
	//    запроса, breaker — системный; нужны оба (architecture.md §6).
	br := breaker.New(breaker.Config{
		ErrorThreshold: cfg.BreakerErrorThreshold,
		Window:         cfg.BreakerWindow,
		ProbeInterval:  cfg.BreakerProbeInterval,
		OnStateChange: func(from, to breaker.State) {
			// Событие редкое, но во время эфира — самое важное: оно означает,
			// что дедуп перестал работать и голоса идут без проверки.
			slog.Warn("дедуп: смена состояния цепи", "from", from.String(), "to", to.String())
		},
	})
	checker := dedup.NewGuardedChecker(dedup.NewRedisStore(rdb), cfg.DedupTimeout, br)

	// 5. Читающее зеркало батчера: Redis → память раз в секунду, память →
	//    Postgres раз в пять секунд. Ни один запрос к результатам не идёт в
	//    Redis напрямую (architecture.md §5.2).
	snapshots := results.NewSnapshotter(
		results.NewRedisSource(countersRdb, cfg.SnapshotInterval),
		cache, cfg.CounterShards, cfg.SnapshotInterval, resultsSettleWindow, slog.Default(),
	)
	persister := results.NewPersister(store, snapshots, cfg.PersistInterval, slog.Default())

	readersCtx, stopReaders := context.WithCancel(context.Background())
	defer stopReaders()
	persisterDone := make(chan struct{})
	go snapshots.Run(readersCtx)
	go func() {
		defer close(persisterDone)
		persister.Run(readersCtx)
	}()

	srv := httpapi.NewServer(cfg, httpapi.Deps{
		Polls:     store,
		Cache:     cache,
		Tokens:    token.NewIssuer(cfg.TokenHMACSecret, cfg.TokenTTL),
		Batcher:   batcher,
		Dedup:     checker,
		Snapshots: snapshots,
	})

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

	err = platform.GracefulShutdown(ctx, httpSrv, cfg.ShutdownTimeout,
		// Финальный flush батчера — то, что превращает плановый рестарт в
		// НУЛЕВУЮ потерю голосов (architecture.md §5.4). Идёт после остановки
		// приёма, иначе счётчики тут же пополнят незавершённые хендлеры.
		func(ctx context.Context) error {
			stopBatcher()
			select {
			case <-batcherDone:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		},
		// Перенос агрегатов останавливается ПОСЛЕ финального flush батчера:
		// иначе последняя порция голосов дойдёт до Redis, но не до Postgres.
		func(ctx context.Context) error {
			stopReaders()
			select {
			case <-persisterDone:
			case <-ctx.Done():
				return ctx.Err()
			}
			return nil
		},
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

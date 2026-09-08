// Команда api — сервис голосований.
//
// Порядок запуска подчинён одному требованию: реагировать во время события
// невозможно, поэтому инстанс обязан выйти на пик уже прогретым
// (architecture.md §1, §6).
package main

func main() {
	// TODO:
	//   cfg := config.Load()
	//
	//   // 1. Пулы — предсозданные и прогретые, не лениво по первому запросу.
	//   db  := platform.NewPostgres(ctx, cfg.PostgresDSN, cfg.PostgresMaxConns)
	//   rdb := platform.NewRedis(ctx, cfg.RedisAddr, cfg.RedisPoolSize)
	//
	//   // 2. Метаданные armed-опросов — в память ДО эфира.
	//   cache := poll.NewCache(poll.NewStore(db))
	//   cache.Warm(ctx)
	//
	//   // 3. Фоновые циклы: батч на запись, батч на чтение, снапшот в Postgres.
	//   go batcher.Run(ctx)      // Redis ← счётчики, каждые 150 мс
	//   go snapshotter.Run(ctx)  // Redis → память, каждую секунду
	//   go persister.Run(ctx)    // память → Postgres, каждые 5 секунд
	//
	//   // 4. Только теперь /healthz отдаёт ready и nginx начинает слать трафик.
	//   srv.ListenAndServe()
	//
	//   // 5. SIGTERM → GracefulShutdown с финальным flush батчера.
}

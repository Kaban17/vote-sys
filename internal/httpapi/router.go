// Package httpapi — HTTP-слой.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/boar/vote-sys/internal/config"
	"github.com/boar/vote-sys/internal/dedup"
	"github.com/boar/vote-sys/internal/poll"
	"github.com/boar/vote-sys/internal/results"
	"github.com/boar/vote-sys/internal/token"
	"github.com/boar/vote-sys/internal/vote"
)

type Server struct {
	cfg *config.Config

	// ready отделяет «процесс жив» от «инстанс готов принимать голоса».
	//
	// Различие не косметическое: пулы прогреваются и метаданные armed-опросов
	// грузятся до того, как nginx начнёт слать трафик. Инстанс, объявивший себя
	// готовым раньше времени, поймает первые секунды пика на установке
	// соединений (architecture.md §6).
	ready atomic.Bool

	logger *slog.Logger
	stats  *accessStats

	polls     *poll.Store
	cache     *poll.Cache
	tokens    *token.Issuer
	batcher   *vote.Batcher
	dedup     dedup.Checker
	snapshots *results.Snapshotter
}

// Deps — внешние зависимости сервера. Структурой, а не позиционными
// аргументами: их станет шесть к шагу 6, и порядок в вызове перестанет
// читаться.
type Deps struct {
	Polls     *poll.Store
	Cache     *poll.Cache
	Tokens    *token.Issuer
	Batcher   *vote.Batcher
	Dedup     dedup.Checker
	Snapshots *results.Snapshotter
}

func NewServer(cfg *config.Config, deps Deps) *Server {
	return &Server{
		cfg:       cfg,
		logger:    slog.Default(),
		stats:     newAccessStats(uint64(cfg.AccessLogSampleN)),
		polls:     deps.Polls,
		cache:     deps.Cache,
		tokens:    deps.Tokens,
		batcher:   deps.Batcher,
		dedup:     deps.Dedup,
		snapshots: deps.Snapshots,
	}
}

// RunStats периодически выводит агрегат трафика по ручкам и обнуляет счётчики.
// Запускается фоном, при отмене контекста печатает последний интервал.
func (s *Server) RunStats(ctx context.Context, every time.Duration) {
	s.stats.Run(ctx, s.logger, every)
}

// FlushStats выводит накопленное немедленно. Нужен при остановке: последние
// секунды события — самые интересные, и терять их из-за незакрытого интервала
// не хочется.
func (s *Server) FlushStats() { s.stats.Flush(s.logger) }

// MarkReady вызывается после прогрева, непосредственно перед началом приёма.
func (s *Server) MarkReady() { s.ready.Store(true) }

// MarkUnready снимает готовность при остановке: nginx перестаёт слать трафик
// раньше, чем сервер начнёт отказывать.
func (s *Server) MarkUnready() { s.ready.Store(false) }

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(s.accessLog)

	// Публичное.
	//
	// Лендинг (GET /p/{id}) сюда не попадает: это чистая статика, её отдаёт
	// nginx/CDN. Разделение принципиальное — см. token.go (architecture.md §5.1).
	r.Post("/api/token", s.handleToken)
	r.Get("/api/polls/{id}", s.handleGetPoll)
	r.Post("/api/polls/{id}/vote", s.handleVote)
	r.Get("/api/polls/{id}/results", s.handlePublicResults)

	// Админское. Аутентификация — статический bearer из окружения; в проде
	// здесь был бы полноценный IdP.
	r.Route("/api/admin", func(r chi.Router) {
		r.Use(s.requireAdmin)
		r.Post("/polls", s.handleCreatePoll)
		r.Get("/polls", s.handleListPolls)
		r.Get("/polls/{id}", s.handleAdminGetPoll)
		r.Get("/polls/{id}/results", s.handleAdminResults)
	})

	r.Get("/healthz", s.handleHealth)
	return r
}

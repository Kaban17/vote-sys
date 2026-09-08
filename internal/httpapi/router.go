// Package httpapi — HTTP-слой.
package httpapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

type Server struct {
	// TODO: poll.Cache, dedup.Checker, vote.Batcher, results.Snapshotter,
	//       token.Issuer, poll.Store, config.
}

func (s *Server) Routes() http.Handler {
	r := chi.NewRouter()

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

package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/boar/vote-sys/internal/config"
	"github.com/boar/vote-sys/internal/dedup"
	"github.com/boar/vote-sys/internal/poll"
	"github.com/boar/vote-sys/internal/token"
	"github.com/boar/vote-sys/internal/vote"
)

// recordingSink запоминает, что батчер отправил бы в Redis.
type recordingSink struct {
	votes  map[string]int64
	voters int64
}

func (r *recordingSink) Push(_ context.Context, _ uuid.UUID, _ int, optionIDs []string, d vote.Delta) error {
	if r.votes == nil {
		r.votes = make(map[string]int64)
	}
	for i, n := range d.Options {
		if n != 0 {
			r.votes[optionIDs[i]] += n
		}
	}
	r.voters += d.Voters
	return nil
}

type staticDedup dedup.Result

func (s staticDedup) Check(context.Context, uuid.UUID, string, time.Duration) dedup.Result {
	return dedup.Result(s)
}

func voteFixture(t *testing.T, res dedup.Result) (*Server, *poll.Poll, *recordingSink, *vote.Batcher) {
	t.Helper()

	start := time.Now().Add(-time.Minute)
	p := &poll.Poll{
		ID: uuid.New(), Question: "вопрос", Kind: poll.KindSingle,
		StartsAt: start, EndsAt: start.Add(time.Hour), Generation: 1,
		Options: []poll.Option{
			{ID: uuid.New(), Text: "A", Position: 0},
			{ID: uuid.New(), Text: "B", Position: 1},
			{ID: uuid.New(), Text: "C", Position: 2},
		},
	}

	cache := poll.NewCache(nil) // store не нужен: опрос кладём напрямую
	cache.Put(p)

	sink := &recordingSink{}
	quietLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	batcher := vote.NewBatcher(sink, 0, time.Hour, quietLogger)

	cfg := &config.Config{
		CounterShards: 1, AccessLogSampleN: 1,
		TokenTTL: time.Hour, VoteGracePeriod: 3 * time.Second,
	}
	srv := quiet(NewServer(cfg, Deps{
		Cache:   cache,
		Tokens:  token.NewIssuer([]byte("секрет"), cfg.TokenTTL),
		Batcher: batcher,
		Dedup:   staticDedup(res),
	}))
	srv.MarkReady()
	return srv, p, sink, batcher
}

func castVote(t *testing.T, srv *Server, p *poll.Poll, optionIDs ...uuid.UUID) *httptest.ResponseRecorder {
	t.Helper()

	tok, err := srv.tokens.Issue(p.ID, time.Now())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	ids := make([]string, len(optionIDs))
	for i, id := range optionIDs {
		ids[i] = `"` + id.String() + `"`
	}
	body := `{"option_ids":[` + strings.Join(ids, ",") + `]}`

	r := httptest.NewRequest(http.MethodPost, "/api/polls/"+p.ID.String()+"/vote", strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: token.CookieName, Value: tok})

	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, r)
	return rec
}

// Принятый голос обязан дойти до счётчика КОНКРЕТНОГО варианта, а не только до
// счётчика участников.
func TestVoteIncrementsChosenOption(t *testing.T) {
	srv, p, sink, batcher := voteFixture(t, dedup.First)

	rec := castVote(t, srv, p, p.Options[1].ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("код %d, тело %s", rec.Code, rec.Body.String())
	}

	batcher.Flush(context.Background())

	if sink.voters != 1 {
		t.Errorf("участников %d, ожидался 1", sink.voters)
	}
	if got := sink.votes[p.Options[1].ID.String()]; got != 1 {
		t.Errorf("голосов за выбранный вариант %d, ожидался 1 (всего записано: %v)", got, sink.votes)
	}
	if got := sink.votes[p.Options[0].ID.String()]; got != 0 {
		t.Errorf("невыбранный вариант получил %d голосов", got)
	}
}

// Инвариант single: число участников равно сумме голосов.
func TestVoteKeepsSingleChoiceInvariant(t *testing.T) {
	srv, p, sink, batcher := voteFixture(t, dedup.First)

	for i := 0; i < 10; i++ {
		if rec := castVote(t, srv, p, p.Options[i%3].ID); rec.Code != http.StatusOK {
			t.Fatalf("голос %d: код %d", i, rec.Code)
		}
	}
	batcher.Flush(context.Background())

	var sum int64
	for _, n := range sink.votes {
		sum += n
	}
	if sum != sink.voters {
		t.Errorf("сумма голосов %d != числу участников %d", sum, sink.voters)
	}
	if sink.voters != 10 {
		t.Errorf("участников %d, ожидалось 10", sink.voters)
	}
}

// Дубль не должен доходить до счётчиков вовсе.
func TestDuplicateVoteIsNotCounted(t *testing.T) {
	srv, p, sink, batcher := voteFixture(t, dedup.Duplicate)

	if rec := castVote(t, srv, p, p.Options[0].ID); rec.Code != http.StatusConflict {
		t.Fatalf("код %d, ожидался 409", rec.Code)
	}
	batcher.Flush(context.Background())

	if sink.voters != 0 || len(sink.votes) != 0 {
		t.Errorf("дубль попал в счётчики: участников %d, голоса %v", sink.voters, sink.votes)
	}
}

// fail-open: голос считается, даже когда дедуп пропущен (architecture.md §6).
func TestBypassedDedupStillCounts(t *testing.T) {
	srv, p, sink, batcher := voteFixture(t, dedup.Bypassed)

	if rec := castVote(t, srv, p, p.Options[2].ID); rec.Code != http.StatusOK {
		t.Fatalf("код %d, ожидался 200", rec.Code)
	}
	batcher.Flush(context.Background())

	if got := sink.votes[p.Options[2].ID.String()]; got != 1 {
		t.Errorf("при bypassed голос не засчитан: %v", sink.votes)
	}
}

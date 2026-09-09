package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/boar/vote-sys/internal/config"
	"github.com/boar/vote-sys/internal/token"
)

func tokenServer(t *testing.T, secure bool) http.Handler {
	t.Helper()
	cfg := &config.Config{
		CounterShards: 1, AccessLogSampleN: 1,
		TokenTTL: time.Hour, CookieSecure: secure,
	}
	srv := quiet(NewServer(cfg, Deps{
		Tokens: token.NewIssuer([]byte("секрет"), cfg.TokenTTL),
	}))
	srv.MarkReady()
	return srv.Routes()
}

func mint(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/token", strings.NewReader(body))
	h.ServeHTTP(rec, r)
	return rec
}

func TestMintSetsCookie(t *testing.T) {
	h := tokenServer(t, false)
	rec := mint(t, h, `{"poll_id":"`+uuid.New().String()+`"}`)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("код %d, want 204", rec.Code)
	}

	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("кук в ответе %d, ожидалась 1", len(cookies))
	}
	c := cookies[0]

	if c.Name != token.CookieName {
		t.Errorf("имя куки %q, want %q", c.Name, token.CookieName)
	}
	if len(c.Value) != token.TokenLen {
		t.Errorf("длина токена %d, want %d", len(c.Value), token.TokenLen)
	}
	if !c.HttpOnly {
		t.Error("кука должна быть HttpOnly: скрипту лендинга токен не нужен")
	}
	if c.Path != "/" {
		t.Errorf("Path = %q, want /", c.Path)
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite = %v, want Lax", c.SameSite)
	}
}

// Минт обязан быть некэшируемым. Структурно это уже обеспечено методом POST, но
// заголовок дублирует защиту: он дёшев, а цена ошибки — весь опрос
// (architecture.md §5.1).
func TestMintIsNotCacheable(t *testing.T) {
	h := tokenServer(t, false)
	rec := mint(t, h, `{"poll_id":"`+uuid.New().String()+`"}`)

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// Главное свойство: два зрителя не должны получить один токен. Именно это
// ломалось бы, если бы кука ставилась на кэшируемом лендинге.
func TestMintGivesDistinctTokens(t *testing.T) {
	h := tokenServer(t, false)
	pollID := uuid.New().String()

	seen := make(map[string]struct{}, 50)
	for n := 0; n < 50; n++ {
		rec := mint(t, h, `{"poll_id":"`+pollID+`"}`)
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("кук в ответе %d", len(cookies))
		}
		if _, dup := seen[cookies[0].Value]; dup {
			t.Fatal("два запроса получили один и тот же токен")
		}
		seen[cookies[0].Value] = struct{}{}
	}
}

func TestMintRejectsBadInput(t *testing.T) {
	h := tokenServer(t, false)

	cases := []struct{ name, body string }{
		{"не json", `не json`},
		{"пустое тело", ``},
		{"poll_id не uuid", `{"poll_id":"не-uuid"}`},
		{"poll_id отсутствует", `{}`},
	}
	for _, c := range cases {
		rec := mint(t, h, c.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: код %d, want 400", c.name, rec.Code)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Errorf("%s: при ошибке кука ставиться не должна", c.name)
		}
	}
}

// Secure обязателен в проде, но на локальном HTTP-стенде браузер такую куку не
// примет — поэтому флаг конфигурируемый, и обе ветки должны работать.
func TestCookieSecureFollowsConfig(t *testing.T) {
	for _, secure := range []bool{true, false} {
		h := tokenServer(t, secure)
		rec := mint(t, h, `{"poll_id":"`+uuid.New().String()+`"}`)
		got := rec.Result().Cookies()[0].Secure
		if got != secure {
			t.Errorf("CookieSecure=%v дал Secure=%v", secure, got)
		}
	}
}

// Повторный минт с валидной кукой НЕ выдаёт новый токен.
//
// Регрессия на реальный обход, найденный при ручной проверке: лендинг
// запрашивает токен при каждой загрузке, и новый затирал куку — перезагрузка
// страницы давала новую личность и новое право голоса. Перезагрузить страницу
// умеет любой зритель, то есть обход был ниже планки, которую задаёт ТЗ.
func TestMintIsIdempotentWithValidCookie(t *testing.T) {
	cfg := &config.Config{
		CounterShards: 1, AccessLogSampleN: 1,
		TokenTTL: time.Hour, CookieSecure: false,
	}
	issuer := token.NewIssuer([]byte("секрет"), cfg.TokenTTL)
	srv := quiet(NewServer(cfg, Deps{Tokens: issuer}))
	srv.MarkReady()
	h := srv.Routes()

	pollID := uuid.New()
	first := mint(t, h, `{"poll_id":"`+pollID.String()+`"}`)
	tok := first.Result().Cookies()[0].Value

	// Второй запрос с уже установленной кукой.
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/token",
		strings.NewReader(`{"poll_id":"`+pollID.String()+`"}`))
	r.AddCookie(&http.Cookie{Name: token.CookieName, Value: tok})
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("код %d, want 204", rec.Code)
	}
	if n := len(rec.Result().Cookies()); n != 0 {
		t.Errorf("при валидной куке выдано %d новых токенов, ожидалось 0", n)
	}
}

// А вот токен от ДРУГОГО опроса не годится: зритель должен получить свой.
func TestMintIssuesNewTokenForDifferentPoll(t *testing.T) {
	cfg := &config.Config{
		CounterShards: 1, AccessLogSampleN: 1,
		TokenTTL: time.Hour, CookieSecure: false,
	}
	issuer := token.NewIssuer([]byte("секрет"), cfg.TokenTTL)
	srv := quiet(NewServer(cfg, Deps{Tokens: issuer}))
	srv.MarkReady()
	h := srv.Routes()

	otherPoll := uuid.New()
	tok, _ := issuer.Issue(otherPoll, time.Now())

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/token",
		strings.NewReader(`{"poll_id":"`+uuid.New().String()+`"}`))
	r.AddCookie(&http.Cookie{Name: token.CookieName, Value: tok})
	h.ServeHTTP(rec, r)

	if len(rec.Result().Cookies()) != 1 {
		t.Error("для другого опроса должен выдаваться новый токен")
	}
}

// Испорченная кука не должна запирать зрителя без токена.
func TestMintReplacesInvalidCookie(t *testing.T) {
	h := tokenServer(t, false)

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/token",
		strings.NewReader(`{"poll_id":"`+uuid.New().String()+`"}`))
	r.AddCookie(&http.Cookie{Name: token.CookieName, Value: "мусор"})
	h.ServeHTTP(rec, r)

	if len(rec.Result().Cookies()) != 1 {
		t.Error("при невалидной куке должен выдаваться новый токен")
	}
}

// Выпущенный токен должен проходить проверку тем же секретом — сквозная связка
// хендлера и пакета token.
func TestMintedTokenVerifies(t *testing.T) {
	cfg := &config.Config{
		CounterShards: 1, AccessLogSampleN: 1,
		TokenTTL: time.Hour, CookieSecure: false,
	}
	issuer := token.NewIssuer([]byte("секрет"), cfg.TokenTTL)
	srv := quiet(NewServer(cfg, Deps{Tokens: issuer}))
	srv.MarkReady()

	pollID := uuid.New()
	rec := mint(t, srv.Routes(), `{"poll_id":"`+pollID.String()+`"}`)
	tok := rec.Result().Cookies()[0].Value

	if _, ok := issuer.Verify(pollID, tok, time.Now()); !ok {
		t.Error("выпущенный токен не прошёл собственную проверку")
	}
	if _, ok := issuer.Verify(uuid.New(), tok, time.Now()); ok {
		t.Error("токен принят для чужого опроса")
	}
}

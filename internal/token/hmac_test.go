package token

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func newIssuer() *Issuer { return NewIssuer([]byte("секрет"), 24*time.Hour) }

func TestIssueThenVerify(t *testing.T) {
	i := newIssuer()
	pollID := uuid.New()
	now := time.Now()

	tok, err := i.Issue(pollID, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if len(tok) != TokenLen {
		t.Errorf("длина токена %d, ожидалась %d", len(tok), TokenLen)
	}

	voter, ok := i.Verify(pollID, tok, now)
	if !ok {
		t.Fatal("свежий токен не прошёл проверку")
	}
	if voter == "" {
		t.Error("VoterID пустой")
	}
}

// VoterID — случайная часть токена, а не токен целиком: при 10 млн ключей
// каждый лишний символ в имени ключа стоит примерно 10 МБ Redis.
func TestVoterIDIsShorterThanToken(t *testing.T) {
	i := newIssuer()
	pollID := uuid.New()

	tok, _ := i.Issue(pollID, time.Now())
	voter, ok := i.Verify(pollID, tok, time.Now())
	if !ok {
		t.Fatal("токен не прошёл проверку")
	}
	if len(voter) >= len(tok) {
		t.Errorf("VoterID (%d) не короче токена (%d)", len(voter), len(tok))
	}
	// 16 байт nonce в base64 без паддинга.
	if want := base64.RawURLEncoding.EncodedLen(nonceLen); len(voter) != want {
		t.Errorf("длина VoterID %d, ожидалась %d", len(voter), want)
	}
}

// Проверка идемпотентна: один и тот же токен всегда даёт одну идентичность,
// иначе дедуп ловил бы одного зрителя как разных.
func TestVoterIDIsStable(t *testing.T) {
	i := newIssuer()
	pollID := uuid.New()
	now := time.Now()

	tok, _ := i.Issue(pollID, now)
	first, _ := i.Verify(pollID, tok, now)
	second, _ := i.Verify(pollID, tok, now.Add(time.Minute))

	if first != second {
		t.Errorf("идентичность нестабильна: %q != %q", first, second)
	}
}

func TestDistinctIssuesGiveDistinctVoters(t *testing.T) {
	i := newIssuer()
	pollID := uuid.New()
	now := time.Now()

	seen := make(map[VoterID]struct{}, 100)
	for n := 0; n < 100; n++ {
		tok, err := i.Issue(pollID, now)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		v, ok := i.Verify(pollID, tok, now)
		if !ok {
			t.Fatal("токен не прошёл проверку")
		}
		if _, dup := seen[v]; dup {
			t.Fatalf("повтор VoterID %q: nonce не случаен", v)
		}
		seen[v] = struct{}{}
	}
}

// Токен привязан к опросу: иначе один минт открывал бы голосование во всех
// опросах сразу.
func TestTokenIsBoundToPoll(t *testing.T) {
	i := newIssuer()
	mine, other := uuid.New(), uuid.New()
	now := time.Now()

	tok, _ := i.Issue(mine, now)
	if _, ok := i.Verify(other, tok, now); ok {
		t.Error("токен от чужого опроса принят")
	}
}

func TestForgedTokensRejected(t *testing.T) {
	i := newIssuer()
	pollID := uuid.New()
	now := time.Now()
	valid, _ := i.Issue(pollID, now)

	// Порча одного символа в подписи и в полезной нагрузке.
	flip := func(s string, pos int) string {
		b := []byte(s)
		if b[pos] == 'A' {
			b[pos] = 'B'
		} else {
			b[pos] = 'A'
		}
		return string(b)
	}

	cases := []struct {
		name string
		tok  string
	}{
		{"пустой", ""},
		{"мусор", strings.Repeat("x", TokenLen)},
		{"короче на символ", valid[:len(valid)-1]},
		{"длиннее на символ", valid + "A"},
		{"не base64", strings.Repeat("!", TokenLen)},
		{"испорчена подпись", flip(valid, TokenLen-3)},
		{"испорчена нагрузка", flip(valid, 20)},
	}
	for _, c := range cases {
		if _, ok := i.Verify(pollID, c.tok, now); ok {
			t.Errorf("%s: подделка принята", c.name)
		}
	}
}

func TestOtherSecretRejected(t *testing.T) {
	pollID := uuid.New()
	now := time.Now()

	tok, _ := NewIssuer([]byte("секрет-один"), time.Hour).Issue(pollID, now)
	if _, ok := NewIssuer([]byte("секрет-два"), time.Hour).Verify(pollID, tok, now); ok {
		t.Error("токен, подписанный чужим ключом, принят")
	}
}

func TestExpiry(t *testing.T) {
	const ttl = time.Hour
	i := NewIssuer([]byte("секрет"), ttl)
	pollID := uuid.New()
	issuedAt := time.Now()

	tok, _ := i.Issue(pollID, issuedAt)

	cases := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"сразу", issuedAt, true},
		{"в пределах срока", issuedAt.Add(ttl - time.Minute), true},
		{"срок истёк", issuedAt.Add(ttl + time.Second), false},
		{"далеко в прошлом", issuedAt.Add(-2 * time.Hour), false},
	}
	for _, c := range cases {
		if _, ok := i.Verify(pollID, tok, c.at); ok != c.want {
			t.Errorf("%s: Verify = %v, want %v", c.name, ok, c.want)
		}
	}
}

// Часы инстансов расходятся, и токен «из будущего» не должен отвергаться:
// зритель в этом не виноват (architecture.md §5.5).
func TestClockSkewTolerated(t *testing.T) {
	i := newIssuer()
	pollID := uuid.New()
	issuedAt := time.Now()

	tok, _ := i.Issue(pollID, issuedAt)

	if _, ok := i.Verify(pollID, tok, issuedAt.Add(-30*time.Second)); !ok {
		t.Error("токен, выпущенный на 30с вперёд, отвергнут: допуск на расхождение часов не работает")
	}
	if _, ok := i.Verify(pollID, tok, issuedAt.Add(-2*clockSkew)); ok {
		t.Error("токен из далёкого будущего должен отвергаться")
	}
}

func BenchmarkIssue(b *testing.B) {
	i := newIssuer()
	pollID := uuid.New()
	now := time.Now()
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		_, _ = i.Issue(pollID, now)
	}
}

func BenchmarkVerify(b *testing.B) {
	i := newIssuer()
	pollID := uuid.New()
	now := time.Now()
	tok, _ := i.Issue(pollID, now)
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		_, _ = i.Verify(pollID, tok, now)
	}
}

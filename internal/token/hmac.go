// Package token выпускает и проверяет анонимный токен голосующего.
//
// Что подпись даёт и чего не даёт (architecture.md §5.1):
//
// Она НЕ защищает от массовой генерации — скрипт дёрнет минт-эндпоинт 10 000 раз
// и получит 10 000 валидных токенов. Она защищает от ОФФЛАЙНОВОЙ генерации: без
// неё атакующему хватило бы uuid4() в цикле, вообще без сети. HMAC превращает
// бесплатную атаку в атаку, требующую сетевого запроса на каждый токен, то есть
// проходящую через rate limit на nginx.
//
// Это ровно тот уровень, который просит ТЗ («достаточная на уровне обычных, не
// технически подкованных пользователей»), и ни на грамм больше. Обход очисткой
// cookie — ожидаемое поведение, а не дефект.
package token

import (
	"time"

	"github.com/google/uuid"
)

// CookieName — имя куки. Используется и в nginx для маршрутизации по хешу
// токена, когда дойдём до локального дедупа (architecture.md §10).
const CookieName = "vote_token"

type Issuer struct {
	secret []byte
	ttl    time.Duration
}

func NewIssuer(secret []byte, ttl time.Duration) *Issuer {
	return &Issuer{secret: secret, ttl: ttl}
}

// Issue выпускает токен для опроса: HMAC от (pollID, случайное значение,
// timestamp). Чистый CPU, ноль обращений к хранилищам — самый дешёвый эндпоинт
// в системе, и первый кандидат на переезд в edge compute.
func (i *Issuer) Issue(pollID uuid.UUID, now time.Time) (string, error) {
	// TODO: payload = pollID | random | unix; token = payload + "." + b64(hmac)
	return "", nil
}

// Verify проверяет подпись и срок. Токен привязан к опросу: токеном от одного
// опроса нельзя проголосовать в другом.
func (i *Issuer) Verify(pollID uuid.UUID, tok string, now time.Time) bool {
	// TODO: hmac.Equal — сравнение за постоянное время.
	return false
}

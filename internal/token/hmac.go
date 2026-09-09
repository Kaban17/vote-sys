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
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"time"

	"github.com/google/uuid"
)

// CookieName — имя куки. Используется и в nginx для маршрутизации по хешу
// токена, когда дойдём до локального дедупа (architecture.md §10).
const CookieName = "vote_token"

const (
	pollIDLen = 16
	nonceLen  = 16
	tsLen     = 8
	macLen    = sha256.Size

	payloadLen = pollIDLen + nonceLen + tsLen
	rawLen     = payloadLen + macLen
)

// TokenLen — длина токена в символах. Проверяется до разбора: отсечь мусор по
// длине дешевле, чем декодировать его.
var TokenLen = base64.RawURLEncoding.EncodedLen(rawLen)

// VoterID — идентичность голосующего внутри одного опроса.
//
// Это случайная часть токена, а не токен целиком, и разница существенна для
// объёма Redis. Дедуп-ключ имеет вид dedup:{poll_id}:{voter_id}; при 10 млн
// ключей каждый лишний символ в имени — это лишние 10 МБ, а токен целиком в
// четыре с лишним раза длиннее своей случайной части (architecture.md §2).
//
// Подставлять сюда nonce безопасно: к моменту вызова подпись уже проверена,
// значит nonce сгенерирован сервером и уникален. Клиент выбрать его не может.
type VoterID string

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
	raw := make([]byte, rawLen)

	copy(raw[:pollIDLen], pollID[:])
	if _, err := rand.Read(raw[pollIDLen : pollIDLen+nonceLen]); err != nil {
		return "", err
	}
	binary.BigEndian.PutUint64(raw[pollIDLen+nonceLen:payloadLen], uint64(now.Unix()))

	mac := hmac.New(sha256.New, i.secret)
	mac.Write(raw[:payloadLen])
	// Sum дописывает подпись прямо в хвост raw: у среза хватает ёмкости, поэтому
	// append не перевыделяет память. На эндпоинте с пиком в сотни тысяч запросов
	// в секунду лишняя аллокация на вызов — не мелочь.
	mac.Sum(raw[:payloadLen])

	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Verify проверяет подпись, привязку к опросу и срок.
//
// Токен привязан к poll_id: токеном от одного опроса нельзя проголосовать в
// другом. Без этого один минт открывал бы голосование во всех опросах сразу.
func (i *Issuer) Verify(pollID uuid.UUID, tok string, now time.Time) (VoterID, bool) {
	if len(tok) != TokenLen {
		return "", false
	}

	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(raw) != rawLen {
		return "", false
	}

	// Подпись проверяется первой: всё остальное — данные из недоверенного
	// источника, и рассуждать о них до проверки MAC бессмысленно.
	mac := hmac.New(sha256.New, i.secret)
	mac.Write(raw[:payloadLen])
	if !hmac.Equal(mac.Sum(nil), raw[payloadLen:]) {
		return "", false
	}

	if !hmac.Equal(raw[:pollIDLen], pollID[:]) {
		return "", false
	}

	issued := time.Unix(int64(binary.BigEndian.Uint64(raw[pollIDLen+nonceLen:payloadLen])), 0)
	if now.Before(issued.Add(-clockSkew)) || now.After(issued.Add(i.ttl)) {
		return "", false
	}

	return VoterID(base64.RawURLEncoding.EncodeToString(raw[pollIDLen : pollIDLen+nonceLen])), true
}

// clockSkew — допуск на расхождение часов между инстансами.
//
// Токен, выпущенный инстансом с чуть убежавшими вперёд часами, не должен
// считаться «выпущенным в будущем» на соседнем инстансе: голос при этом
// отвергался бы без всякой вины зрителя (architecture.md §5.5).
const clockSkew = time.Minute

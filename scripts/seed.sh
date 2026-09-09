#!/usr/bin/env sh
# Создаёт демо-опрос через админский API и печатает его идентификатор.
#
# starts_at ставится на несколько секунд в прошлое, чтобы опрос был открыт сразу
# и его можно было потрогать руками. Длительность по умолчанию — 5 минут, а не
# минута ролика: настоящее окно эфира слишком короткое для ручной проверки.
# Для профиля, приближенного к эфиру: DURATION=60 ./scripts/seed.sh
set -eu

BASE="${BASE:-http://localhost:8080}"
ADMIN_TOKEN="${ADMIN_BEARER_TOKEN:-dev-admin-token}"
DURATION="${DURATION:-300}"
KIND="${KIND:-single}"

now=$(date -u +%s)
starts_at=$(date -u -d "@$((now - 5))" +%Y-%m-%dT%H:%M:%SZ)
ends_at=$(date -u -d "@$((now + DURATION))" +%Y-%m-%dT%H:%M:%SZ)

body=$(cat <<JSON
{
  "question": "Какой вариант вы поддерживаете?",
  "kind": "$KIND",
  "starts_at": "$starts_at",
  "ends_at": "$ends_at",
  "options": ["Вариант A", "Вариант B", "Вариант C"]
}
JSON
)

resp=$(curl -sS -X POST "$BASE/api/admin/polls" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d "$body")

# Первый "id" в ответе — идентификатор опроса: поля структуры сериализуются в
# порядке объявления, и ID стоит перед списком вариантов.
#
# Именно grep, а не sed: в BRE нет нежадного повтора, поэтому ведущий .* дошёл бы
# до ПОСЛЕДНЕГО "id" в ответе, то есть до идентификатора последнего варианта.
poll_id=$(printf '%s' "$resp" \
  | grep -o '"id":"[0-9a-fA-F-]\{36\}"' \
  | head -1 | cut -d'"' -f4)

if [ -z "$poll_id" ]; then
  echo "не удалось создать опрос:" >&2
  echo "$resp" >&2
  exit 1
fi

echo "$poll_id"
echo "опрос открыт до $ends_at" >&2
echo "  посмотреть:  curl -H 'Authorization: Bearer $ADMIN_TOKEN' $BASE/api/admin/polls/$poll_id" >&2

#!/usr/bin/env sh
# Создаёт демо-опрос через админский API.
#
# starts_at ставится в прошлое, ends_at — на минуту вперёд, чтобы повторить
# длительность ролика.
set -eu

BASE="${BASE:-http://localhost:8080}"
ADMIN_TOKEN="${ADMIN_BEARER_TOKEN:-dev-admin-token}"

# TODO: подставить реальные даты и распарсить id из ответа.
curl -sS -X POST "$BASE/api/admin/polls" \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
        "question": "Какой вариант вы поддерживаете?",
        "kind": "single",
        "starts_at": "REPLACE",
        "ends_at": "REPLACE",
        "options": ["Вариант A", "Вариант B"]
      }'

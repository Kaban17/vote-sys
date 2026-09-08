-- Модель данных: architecture.md §7.
--
-- Сырые голоса не хранятся нигде: ни таблицы, ни лога, ни очереди с
-- индивидуальными событиями. Обезличенность обеспечена структурно, а не
-- политикой доступа — связать голос с человеком невозможно, потому что такой
-- записи не существует.
--
-- IF NOT EXISTS: миграция применяется дважды — compose прогоняет её через
-- initdb при первом старте, и `make migrate` должен оставаться безопасным на
-- уже поднятой базе. Без этого повторный запуск сыпал ошибками, но возвращал
-- нулевой код, то есть прятал настоящие поломки.

CREATE TABLE IF NOT EXISTS polls (
    id          uuid PRIMARY KEY,
    question    text        NOT NULL,
    kind        text        NOT NULL CHECK (kind IN ('single', 'multiple')),
    max_choices int         CHECK (max_choices IS NULL OR max_choices > 0),
    starts_at   timestamptz NOT NULL,
    ends_at     timestamptz NOT NULL,
    -- Номер прогона. Позволяет счётчику уменьшиться в обход GREATEST:
    -- нужен для восстановления после потери Redis и для чистого перезапуска
    -- стенда (architecture.md §5.7).
    generation  int         NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT ends_after_start CHECK (ends_at > starts_at),
    CONSTRAINT max_choices_only_for_multiple
        CHECK ((kind = 'multiple') OR (max_choices IS NULL))
);

CREATE TABLE IF NOT EXISTS poll_options (
    id       uuid PRIMARY KEY,
    poll_id  uuid NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    text     text NOT NULL,
    position int  NOT NULL,

    UNIQUE (poll_id, position)
);

CREATE INDEX IF NOT EXISTS poll_options_poll_id_idx ON poll_options (poll_id, position);

-- Агрегаты: снапшот счётчиков из Redis. Пишется абсолютным значением, а не
-- дельтой, поэтому повторная запись безопасна и любой инстанс может её сделать
-- без выборов лидера (architecture.md §5.7).
CREATE TABLE IF NOT EXISTS poll_results (
    poll_id    uuid   NOT NULL REFERENCES polls(id) ON DELETE CASCADE,
    option_id  uuid   NOT NULL REFERENCES poll_options(id) ON DELETE CASCADE,
    votes      bigint NOT NULL CHECK (votes >= 0),
    generation int    NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (poll_id, option_id)
);

-- Число проголосовавших. Отдельно от poll_results, потому что при kind =
-- 'multiple' один запрос увеличивает несколько счётчиков и sum(votes)
-- перестаёт быть числом участников (architecture.md §5.8).
CREATE TABLE IF NOT EXISTS poll_totals (
    poll_id    uuid   PRIMARY KEY REFERENCES polls(id) ON DELETE CASCADE,
    voters     bigint NOT NULL CHECK (voters >= 0),
    generation int    NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

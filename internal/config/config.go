// Package config — конфигурация из окружения. Значения и их смысл описаны в
// .env.example.
package config

import "time"

type Config struct {
	HTTPAddr string

	// InstanceOrdinal — порядковый номер инстанса; шард счётчиков — ординал по
	// модулю CounterShards. Ординал, а не hash(instance_id): хеш без нужды
	// схлопнул бы два инстанса в один шард (architecture.md §7).
	InstanceOrdinal int
	CounterShards   int

	PostgresDSN      string
	PostgresMaxConns int32

	RedisAddr     string
	RedisPoolSize int

	// Дедуп — единственный синхронный round-trip и потолок системы
	// (architecture.md §2, §6).
	DedupTimeout          time.Duration
	BreakerErrorThreshold int
	BreakerWindow         time.Duration
	BreakerProbeInterval  time.Duration

	FlushInterval  time.Duration
	FlushThreshold int64

	SnapshotInterval time.Duration
	PersistInterval  time.Duration

	// VoteGracePeriod применяется только к приёму. Клиенту ends_at отдаётся без
	// него (architecture.md §5.5).
	VoteGracePeriod time.Duration

	TokenHMACSecret []byte
	TokenTTL        time.Duration

	AdminBearerToken string
}

func Load() (*Config, error) {
	// TODO: os.Getenv с дефолтами и валидацией.
	// Секреты (TOKEN_HMAC_SECRET, ADMIN_BEARER_TOKEN) обязаны быть заданы явно —
	// падать на старте лучше, чем работать с дефолтным ключом подписи.
	return nil, nil
}

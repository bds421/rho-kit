## 1.6.0 (2026-04-06)

### 🧱 Updated Dependencies

- Updated infra/sqldb/gormdb/gormpostgres to 1.3.0
- Updated infra/sqldb/gormdb/gormmysql to 1.3.0
- Updated infra/messaging/amqpbackend to 1.2.0
- Updated observability/auditlog to 1.1.0
- Updated observability/logging to 1.1.0
- Updated observability/tracing to 1.1.0
- Updated observability/health to 1.1.0
- Updated observability/slo to 0.2.0
- Updated runtime/lifecycle to 1.1.0
- Updated runtime/eventbus to 1.2.0
- Updated security/jwtutil to 1.1.0
- Updated security/netutil to 1.2.0
- Updated infra/messaging to 1.2.0
- Updated infra/storage to 1.1.0
- Updated runtime/cron to 1.1.0
- Updated core/config to 1.2.0
- Updated infra/redis to 1.2.0
- Updated infra/sqldb to 1.3.0
- Updated grpcx to 0.2.0
- Updated httpx to 1.5.0

# Changelog

## [1.5.0](https://github.com/bds421/rho-kit/compare/app/v1.4.0...app/v1.5.0) (2026-03-28)


### Features

* **app:** use Driver interface for database modules and add read replica support ([#46](https://github.com/bds421/rho-kit/issues/46)) ([02c7f8b](https://github.com/bds421/rho-kit/commit/02c7f8b4529455c39fb1d7d9b36c1e672f67c52d))

## [1.4.0](https://github.com/bds421/rho-kit/compare/app/v1.3.0...app/v1.4.0) (2026-03-28)


### Features

* **app:** export NewGRPCModule, remove WithGRPC builder method ([#52](https://github.com/bds421/rho-kit/issues/52)) ([f251f37](https://github.com/bds421/rho-kit/commit/f251f37a8826c47204d5290a2de27bf464c7d7c2))

## [1.3.0](https://github.com/bds421/rho-kit/compare/app/v1.2.0...app/v1.3.0) (2026-03-28)


### Features

* **app:** add WithGRPC builder integration ([#43](https://github.com/bds421/rho-kit/issues/43)) ([191c239](https://github.com/bds421/rho-kit/commit/191c2395e49ecd319b3b7ff14cbaf5a05420c2d6))

## [1.2.0](https://github.com/bds421/rho-kit/compare/app/v1.1.1...app/v1.2.0) (2026-03-27)


### Features

* **app:** add WithSLO builder option ([#37](https://github.com/bds421/rho-kit/issues/37)) ([0b2b8da](https://github.com/bds421/rho-kit/commit/0b2b8dac980dabb98fcbf1b620d98dbd743d00b0))

## [1.1.1](https://github.com/bds421/rho-kit/compare/app/v1.1.0...app/v1.1.1) (2026-03-27)


### Bug Fixes

* add missing plan items (eventbus lifecycle, config watch, propagation docs) ([#35](https://github.com/bds421/rho-kit/issues/35)) ([a2cac81](https://github.com/bds421/rho-kit/commit/a2cac817c3bd18ffe1664afc6faafdc0f3a189e3))

## [1.1.0](https://github.com/bds421/rho-kit/compare/app/v1.0.0...app/v1.1.0) (2026-03-27)


### Features

* **app:** add module system for builder extensibility ([#27](https://github.com/bds421/rho-kit/issues/27)) ([59216a3](https://github.com/bds421/rho-kit/commit/59216a31487a56ccbd1c84c9ac1cbf38a9fd2943))

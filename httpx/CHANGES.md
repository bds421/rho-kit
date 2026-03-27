# Changelog

## [1.4.0](https://github.com/bds421/rho-kit/compare/httpx/v1.3.0...httpx/v1.4.0) (2026-03-27)


### Features

* **httpx:** add SLO HTTP handler and healthhttp wiring ([#33](https://github.com/bds421/rho-kit/issues/33)) ([55a93eb](https://github.com/bds421/rho-kit/commit/55a93ebdb70c417f0e804a25eeaca9751fa84bb6))

## [1.3.0](https://github.com/bds421/rho-kit/compare/httpx/v1.2.1...httpx/v1.3.0) (2026-03-27)


### Features

* add degradation support to rate limiter and distributed lock ([#30](https://github.com/bds421/rho-kit/issues/30)) ([2038259](https://github.com/bds421/rho-kit/commit/2038259a72f397e08fbf9dd6d49799e222b3bf2b))

## [1.2.1](https://github.com/bds421/rho-kit/compare/httpx/v1.2.0...httpx/v1.2.1) (2026-03-26)


### Bug Fixes

* bump httpx internal deps to v1.1.0 ([#22](https://github.com/bds421/rho-kit/issues/22)) ([155a590](https://github.com/bds421/rho-kit/commit/155a59090fd6b520b2b40e3f16c03888af1b4c53))

## [1.2.0](https://github.com/bds421/rho-kit/compare/httpx/v1.1.0...httpx/v1.2.0) (2026-03-26)


### Features

* add correlation ID middleware with propagation helpers ([#6](https://github.com/bds421/rho-kit/issues/6)) ([96d0328](https://github.com/bds421/rho-kit/commit/96d0328306086f2c9060e8aaf2afd949cc1ae82e))
* add request signing package for inter-service auth ([#13](https://github.com/bds421/rho-kit/issues/13)) ([812f57a](https://github.com/bds421/rho-kit/commit/812f57a270fc7e405dadabbef98f6e5f5973cc42))
* **apperror:** add Retryable interface, enhance UnavailableError ([#20](https://github.com/bds421/rho-kit/issues/20)) ([473e687](https://github.com/bds421/rho-kit/commit/473e68701a3086e6ff80738930d9708da2129574))


### Bug Fixes

* **csrf:** reject whitespace-only API keys, extract bearerPrefixLen, add predicate tests ([#18](https://github.com/bds421/rho-kit/issues/18)) ([1a90f8c](https://github.com/bds421/rho-kit/commit/1a90f8c51fe4e451dfbc1520aaef2e0b68cf6324))
* **httpx:** improve deadline budget docs and add expired-context test ([#19](https://github.com/bds421/rho-kit/issues/19)) ([8bfe9b1](https://github.com/bds421/rho-kit/commit/8bfe9b15873ca86c5e235db1966d05c6b1feaea4))

## [1.1.0](https://github.com/bds421/rho-kit/compare/httpx-v1.0.0...httpx-v1.1.0) (2026-03-24)


### Features

* **csrf:** add WithSkipCheck option with HasBearerToken and HasAPIKey predicates ([#8](https://github.com/bds421/rho-kit/issues/8)) ([7888c49](https://github.com/bds421/rho-kit/commit/7888c49b7701aa533edb9e05cdaea0f7f5a22933))
* **httpx:** add deadline budget propagation for resilient HTTP client ([#11](https://github.com/bds421/rho-kit/issues/11)) ([7dd27c7](https://github.com/bds421/rho-kit/commit/7dd27c7379a915fac71105e2a396693dff8eee4d))


### Bug Fixes

* **csrf:** case-insensitive Bearer check and improved test coverage ([#14](https://github.com/bds421/rho-kit/issues/14)) ([bc703f5](https://github.com/bds421/rho-kit/commit/bc703f5509ff8091e1652d5d62ee4b34fab6b5e4))

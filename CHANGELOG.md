# Changelog

## [0.4.0](https://github.com/fclairamb/ntnsync/compare/v0.3.1...v0.4.0) (2026-01-28)


### Features

* **store:** add streaming file downloads to reduce memory usage ([#18](https://github.com/fclairamb/ntnsync/issues/18)) ([aaa730d](https://github.com/fclairamb/ntnsync/commit/aaa730d80bb09f215f628fc8159851ce0370de5b))
* **sync:** transliterate accented characters in filenames ([#21](https://github.com/fclairamb/ntnsync/issues/21)) ([2975e22](https://github.com/fclairamb/ntnsync/commit/2975e222e3937d0a18d720cff1c0c5c222ebcf9e))


### Performance Improvements

* optimize queue processing performance ([#20](https://github.com/fclairamb/ntnsync/issues/20)) ([c721a3a](https://github.com/fclairamb/ntnsync/commit/c721a3a02fe46fca39878cdd17b60ea94af86679))

## [0.3.1](https://github.com/fclairamb/ntnsync/compare/v0.3.0...v0.3.1) (2026-01-25)


### Bug Fixes

* **store:** handle non-fast-forward push errors by pulling first ([#15](https://github.com/fclairamb/ntnsync/issues/15)) ([1c83275](https://github.com/fclairamb/ntnsync/commit/1c832753c4cfc7d0c4efc5b6253cc16acd4132a0))

## [0.3.0](https://github.com/fclairamb/ntnsync/compare/v0.2.0...v0.3.0) (2026-01-25)


### Features

* **webhook:** add /health endpoint for health checks ([#13](https://github.com/fclairamb/ntnsync/issues/13)) ([00eb212](https://github.com/fclairamb/ntnsync/commit/00eb212394c23a2bd69b3fce9990551be0d75f68))


### Bug Fixes

* **deps:** update module github.com/knadh/koanf/providers/env to v2 ([#11](https://github.com/fclairamb/ntnsync/issues/11)) ([8548d96](https://github.com/fclairamb/ntnsync/commit/8548d964f41317e43c83a00752ee20f313d3ce11))

## [0.2.0](https://github.com/fclairamb/ntnsync/compare/v0.1.0...v0.2.0) (2026-01-25)


### Features

* **logging:** add JSON log format support via NTN_LOG_FORMAT ([#3](https://github.com/fclairamb/ntnsync/issues/3)) ([399cbb6](https://github.com/fclairamb/ntnsync/commit/399cbb68fac9c8ffcfae371d80720b2003c9bb49))


### Bug Fixes

* **deps:** update module github.com/knadh/koanf/v2 to v2.3.2 ([#4](https://github.com/fclairamb/ntnsync/issues/4)) ([881feb0](https://github.com/fclairamb/ntnsync/commit/881feb0cbe0ba46c038a5fb7194e66d351057989))
* **deps:** update module github.com/urfave/cli/v3 to v3.6.2 ([#5](https://github.com/fclairamb/ntnsync/issues/5)) ([4dda82f](https://github.com/fclairamb/ntnsync/commit/4dda82faafa36628306c15c223d32cfb4225b379))
* **docker:** Fixing entry points ([#9](https://github.com/fclairamb/ntnsync/issues/9)) ([c91d618](https://github.com/fclairamb/ntnsync/commit/c91d618a8526d774da18a93b71ef0aa75ede0cc4))

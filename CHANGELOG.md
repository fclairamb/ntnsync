# Changelog

## [0.6.1](https://github.com/fclairamb/ntnsync/compare/v0.6.0...v0.6.1) (2026-02-11)


### Bug Fixes

* **deps:** update module github.com/go-git/go-git/v5 to v5.16.5 ([#45](https://github.com/fclairamb/ntnsync/issues/45)) ([53dff57](https://github.com/fclairamb/ntnsync/commit/53dff574826d4ba2b8befa2317251108d440b120))
* **deps:** update module golang.org/x/text to v0.34.0 ([#46](https://github.com/fclairamb/ntnsync/issues/46)) ([115947f](https://github.com/fclairamb/ntnsync/commit/115947f5a155e236a8e6c0154c11da94f55daadc))
* **sync:** discover child_database blocks during progressive indexing ([#48](https://github.com/fclairamb/ntnsync/issues/48)) ([12c2d92](https://github.com/fclairamb/ntnsync/commit/12c2d9220c144f863cd2a32a79c10c2b173afc62))

## [0.6.0](https://github.com/fclairamb/ntnsync/compare/v0.5.0...v0.6.0) (2026-02-04)


### Features

* **converter:** add download_duration to frontmatter ([#35](https://github.com/fclairamb/ntnsync/issues/35)) ([953068f](https://github.com/fclairamb/ntnsync/commit/953068f19ad935c7d5b71a291aa6747eb6085f2c))
* **notion:** add user info format with name, email, and short ID ([#29](https://github.com/fclairamb/ntnsync/issues/29)) ([67b9de3](https://github.com/fclairamb/ntnsync/commit/67b9de36d0f50f12359f0eaa912d8d38bc39fb05))
* **notion:** upgrade to API version 2025-09-03 ([#31](https://github.com/fclairamb/ntnsync/issues/31)) ([7e44184](https://github.com/fclairamb/ntnsync/commit/7e44184b0071540aff90e16d6a7811ee50f0412c))
* **sync:** add ntnsync_version to all generated files ([#27](https://github.com/fclairamb/ntnsync/issues/27)) ([21539ee](https://github.com/fclairamb/ntnsync/commit/21539eeb5911fe7cdecaffebec13b5156035d984))
* **sync:** add user enrichment to cache created_by and last_edited_by names ([#43](https://github.com/fclairamb/ntnsync/issues/43)) ([6b972cf](https://github.com/fclairamb/ntnsync/commit/6b972cf9c41aab2054da046a973258e39693216b))
* **sync:** change root.md format to task list for GitHub interactivity ([#33](https://github.com/fclairamb/ntnsync/issues/33)) ([4a90370](https://github.com/fclairamb/ntnsync/commit/4a90370eff0af14746803ba5e653d32cb816088a))
* **sync:** replace add command with root.md manifest file ([#30](https://github.com/fclairamb/ntnsync/issues/30)) ([df4477b](https://github.com/fclairamb/ntnsync/commit/df4477bef2e445e60ab843b3358dfed22b430a16))


### Bug Fixes

* **store:** handle diverged branches during pull ([#36](https://github.com/fclairamb/ntnsync/issues/36)) ([ba7b042](https://github.com/fclairamb/ntnsync/commit/ba7b042af9c9048b1830a254c966e01522b29cda))
* **store:** update remote origin URL when it changes ([#32](https://github.com/fclairamb/ntnsync/issues/32)) ([4af69da](https://github.com/fclairamb/ntnsync/commit/4af69da93922bd80c243b0b8907f19fd4e487ae5))
* **store:** use configured branch for git push operations ([#42](https://github.com/fclairamb/ntnsync/issues/42)) ([203f5ae](https://github.com/fclairamb/ntnsync/commit/203f5ae97535edb1a13750e948e315e32fcf00f1))
* **sync:** filter new items by root and queue on startup ([#40](https://github.com/fclairamb/ntnsync/issues/40)) ([8570ce8](https://github.com/fclairamb/ntnsync/commit/8570ce8e088b90753d46b8323e32eb5697cae338))
* **sync:** filter new pages by root.md during pull ([#34](https://github.com/fclairamb/ntnsync/issues/34)) ([eb351eb](https://github.com/fclairamb/ntnsync/commit/eb351ebfba6e0b98ca0c377e8d85d4c906937697))
* **sync:** filter new pages/databases by root.md during processing ([#39](https://github.com/fclairamb/ntnsync/issues/39)) ([d02f2ff](https://github.com/fclairamb/ntnsync/commit/d02f2ff5846064e3d76d73af4a705a3c5d147a6a))
* **sync:** preserve IsRoot and Enabled flags during page/database processing ([#41](https://github.com/fclairamb/ntnsync/issues/41)) ([588c1fd](https://github.com/fclairamb/ntnsync/commit/588c1fdfef9202951b8da53d1d58a3365b94f93c))


### Code Refactoring

* **logging:** use milliseconds for all duration logging ([#37](https://github.com/fclairamb/ntnsync/issues/37)) ([bb99e33](https://github.com/fclairamb/ntnsync/commit/bb99e335ed37a6bde6fe6250d961b6996f8b57af))
* **sync:** reduce code duplication across page/database processing ([#44](https://github.com/fclairamb/ntnsync/issues/44)) ([09d7ec4](https://github.com/fclairamb/ntnsync/commit/09d7ec412a231ccb943b6ccce51960e1ef4c62e9))

## [0.5.0](https://github.com/fclairamb/ntnsync/compare/v0.4.0...v0.5.0) (2026-01-29)


### Features

* **sync:** add block discovery depth limit via NTN_BLOCK_DEPTH ([#24](https://github.com/fclairamb/ntnsync/issues/24)) ([fbfcd26](https://github.com/fclairamb/ntnsync/commit/fbfcd2675123da22f3f37db6c4c2292f1868bd2b))
* **sync:** add extended page properties to frontmatter ([#26](https://github.com/fclairamb/ntnsync/issues/26)) ([5271b94](https://github.com/fclairamb/ntnsync/commit/5271b94d8d9aea6bb120edaedcf0a8d1a7c4d870))

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

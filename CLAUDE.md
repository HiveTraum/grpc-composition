# CLAUDE.md — grpc-composition-docs

## What this is

Документация по продуктовому видению **grpc-composition** — тонкого Go-фреймворка для построения API Composition / BFF layer поверх unary gRPC сервисов.

Главный документ — `README.md`. Здесь живёт vision, спецификация, scope, roadmap, open questions. Код самого фреймворка живёт в отдельном репозитории (TBD).

## Operating rules

- Это **docs-only** репозиторий. **Никакого Go-кода** здесь не предполагается — code examples внутри markdown допустимы, но не реализация.
- Все правки — markdown.
- Язык документа — русский (по предпочтению автора). Code examples — Go, идентификаторы и API на английском.
- При изменении vision синхронно обновлять разделы Scope, Roadmap и Open Questions — они зависимы.
- Не создавать дополнительные документы без необходимости. Один источник истины — `README.md`.

## Stack

- markdown
- никакого build system

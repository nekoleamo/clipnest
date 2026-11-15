//! schema 与迁移（T2 实现）。
//!
//! 约定：`PRAGMA user_version` 是唯一的版本源，`migrate()` 只往前走；
//! `items_fts` 用外部内容表 + 触发器与 `items` 同步。

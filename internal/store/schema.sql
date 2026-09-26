-- 认字程序数据库结构。版本 1。
--
-- 日期一律用 TEXT 存 'YYYY-MM-DD'（学习日，非时间戳）：
-- 「第二天」「第 7 天」是自然日概念，字符串比较即日期比较，
-- 且直接可读、便于用 sqlite3 命令行排查。
-- 时间戳列（created_at 等）只用于审计，不参与调度判定。

CREATE TABLE child (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  birth_year INTEGER,
  archived   INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
  created_at TEXT NOT NULL DEFAULT (datetime('now', 'localtime'))
);

-- 学期挂在孩子下：两个孩子可能读不同年级。
-- 待复习队列跨所有学期，新字只从当前学期放出。
CREATE TABLE semester (
  id         INTEGER PRIMARY KEY,
  child_id   INTEGER NOT NULL REFERENCES child(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  sort_order INTEGER NOT NULL DEFAULT 0,
  is_current INTEGER NOT NULL DEFAULT 0 CHECK (is_current IN (0, 1)),
  created_at TEXT NOT NULL DEFAULT (datetime('now', 'localtime')),
  UNIQUE (child_id, name)
);

-- 每个孩子最多一个当前学期。
CREATE UNIQUE INDEX ux_semester_current ON semester(child_id) WHERE is_current = 1;

CREATE TABLE settings (
  child_id INTEGER NOT NULL REFERENCES child(id) ON DELETE CASCADE,
  key      TEXT NOT NULL,
  value    TEXT NOT NULL,
  PRIMARY KEY (child_id, key)
);

-- 导入批次：记录每次导入的来源与统计，便于回看和按来源筛选。
CREATE TABLE import_batch (
  id          INTEGER PRIMARY KEY,
  child_id    INTEGER NOT NULL REFERENCES child(id) ON DELETE CASCADE,
  semester_id INTEGER NOT NULL REFERENCES semester(id) ON DELETE CASCADE,
  source_name TEXT NOT NULL,
  source_kind TEXT NOT NULL CHECK (source_kind IN ('paste', 'upload', 'manual')),
  raw_sha256  TEXT,
  raw_bytes   INTEGER NOT NULL DEFAULT 0,
  char_count  INTEGER NOT NULL,
  skip_json   TEXT,
  created_at  TEXT NOT NULL DEFAULT (datetime('now', 'localtime'))
);

CREATE INDEX ix_batch_semester ON import_batch(semester_id, created_at DESC);

-- 汉字主表，按孩子隔离。
-- seq 是导入时的首次出现顺序，决定「新字」放出的先后。
CREATE TABLE hanzi (
  id               INTEGER PRIMARY KEY,
  child_id         INTEGER NOT NULL REFERENCES child(id) ON DELETE CASCADE,
  ch               TEXT NOT NULL CHECK (length(ch) BETWEEN 1 AND 2),
  codepoint        INTEGER NOT NULL,
  seq              INTEGER NOT NULL DEFAULT 0,
  semester_id      INTEGER NOT NULL REFERENCES semester(id) ON DELETE CASCADE,
  status           TEXT NOT NULL DEFAULT 'new'
                   CHECK (status IN ('new', 'learning', 'reviewing', 'mastered', 'suspended')),
  -- pinyin 是常用读音（带声调），pinyin_all 是全部读音，空格分隔。
  -- 导入时算好存下来，复习时直接读，不用每次查字典。
  pinyin           TEXT NOT NULL DEFAULT '',
  pinyin_all       TEXT NOT NULL DEFAULT '',
  first_learned_on TEXT,
  created_at       TEXT NOT NULL DEFAULT (datetime('now', 'localtime'))
);

-- 同一个孩子同一个字只能有一行——这是导入幂等的依据。
CREATE UNIQUE INDEX ux_hanzi_child_ch ON hanzi(child_id, ch);
CREATE INDEX ix_hanzi_semester ON hanzi(semester_id, seq);
CREATE INDEX ix_hanzi_child_status ON hanzi(child_id, status);

-- 字 ↔ 批次：重复导入同一份文本时只增加关联，不重复插字。
CREATE TABLE hanzi_source (
  hanzi_id      INTEGER NOT NULL REFERENCES hanzi(id) ON DELETE CASCADE,
  batch_id      INTEGER NOT NULL REFERENCES import_batch(id) ON DELETE CASCADE,
  seen_count    INTEGER NOT NULL DEFAULT 1,
  first_seen_at TEXT NOT NULL DEFAULT (datetime('now', 'localtime')),
  PRIMARY KEY (hanzi_id, batch_id)
);

-- 复习调度状态，与 hanzi 一对一。
-- due_on 为 NULL 表示从未学过（走「新字」通道）。
CREATE TABLE review_state (
  hanzi_id       INTEGER PRIMARY KEY REFERENCES hanzi(id) ON DELETE CASCADE,
  child_id       INTEGER NOT NULL REFERENCES child(id) ON DELETE CASCADE,
  level          INTEGER NOT NULL DEFAULT 0,
  interval_days  INTEGER NOT NULL DEFAULT 0,
  due_on         TEXT,
  last_review_on TEXT,
  first_learned_on TEXT,
  correct_count  INTEGER NOT NULL DEFAULT 0,
  wrong_count    INTEGER NOT NULL DEFAULT 0,
  streak_correct INTEGER NOT NULL DEFAULT 0,
  streak_wrong   INTEGER NOT NULL DEFAULT 0,
  lapses         INTEGER NOT NULL DEFAULT 0,
  updated_at     TEXT NOT NULL DEFAULT (datetime('now', 'localtime'))
);

-- 队列查询唯一依赖的索引。
CREATE INDEX ix_rs_child_due ON review_state(child_id, due_on, last_review_on);
CREATE INDEX ix_rs_child_hanzi ON review_state(child_id, hanzi_id);

-- 复习流水：每次点击一条，用于统计与「为什么今天出这个字」。
CREATE TABLE review_log (
  id                INTEGER PRIMARY KEY,
  child_id          INTEGER NOT NULL REFERENCES child(id) ON DELETE CASCADE,
  hanzi_id          INTEGER NOT NULL REFERENCES hanzi(id) ON DELETE CASCADE,
  session_id        TEXT,
  reviewed_on       TEXT NOT NULL,
  reviewed_at       TEXT NOT NULL DEFAULT (datetime('now', 'localtime')),
  result            TEXT NOT NULL CHECK (result IN ('known', 'unknown')),
  reason            TEXT NOT NULL CHECK (reason IN
                      ('new', 'due', 'anchor1', 'anchor2', 'anchor6', 'sweep', 'manual')),
  level_before      INTEGER NOT NULL,
  level_after       INTEGER NOT NULL,
  interval_after    INTEGER NOT NULL,
  due_before        TEXT,
  due_after         TEXT,
  elapsed_days      INTEGER NOT NULL DEFAULT 0,
  latency_ms        INTEGER,
  -- 撤销所需的完整前像。存 JSON 而不是逐列存 before 值，
  -- 避免将来加列时漏改。
  state_before_json TEXT NOT NULL
);

CREATE INDEX ix_log_child_day ON review_log(child_id, reviewed_on);
CREATE INDEX ix_log_hanzi_time ON review_log(hanzi_id, reviewed_at DESC);

-- 防连点、防双标签页重复提交：同一 session 内同一个字只记一次。
CREATE UNIQUE INDEX ux_log_session_hanzi ON review_log(session_id, hanzi_id)
  WHERE session_id IS NOT NULL;

-- 每日配额账本：控制新字放出量，并记录当天完成情况。
CREATE TABLE day_session (
  child_id   INTEGER NOT NULL REFERENCES child(id) ON DELETE CASCADE,
  day        TEXT NOT NULL,
  new_served INTEGER NOT NULL DEFAULT 0,
  answered   INTEGER NOT NULL DEFAULT 0,
  known      INTEGER NOT NULL DEFAULT 0,
  unknown    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (child_id, day)
);

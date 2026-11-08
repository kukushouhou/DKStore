// Package store 封装 SQLite 访问与 schema 迁移。
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // 纯 Go SQLite 驱动，无需 CGO
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store 持有数据库连接。
type Store struct {
	DB *sql.DB
}

// Open 打开（或创建）数据目录下的 dkstore.db，并执行迁移。
func Open(ctx context.Context, dataDir string) (*Store, error) {
	path := filepath.Join(dataDir, "dkstore.db")
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", filepath.ToSlash(path))
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// SQLite 写入串行化，单连接最简单可靠。
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	s := &Store{DB: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.DB.Close() }

// SchemaVersion 返回当前已应用的最高迁移版本。
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

type migration struct {
	version int
	name    string
}

// migrate 按文件名前缀的版本号（NNNN_xxx.sql）顺序执行未应用的迁移，每个迁移一个事务。
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL DEFAULT (unixepoch())
	)`); err != nil {
		return fmt.Errorf("初始化迁移表失败: %w", err)
	}

	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	var list []migration
	for _, e := range entries {
		prefix, _, ok := strings.Cut(e.Name(), "_")
		if !ok {
			continue
		}
		v, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("迁移文件名不合法: %s", e.Name())
		}
		list = append(list, migration{version: v, name: e.Name()})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].version < list[j].version })

	current, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	for _, m := range list {
		if m.version <= current {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + m.name)
		if err != nil {
			return err
		}
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("执行迁移 %s 失败: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (?)`, m.version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

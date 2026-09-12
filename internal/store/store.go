// Package store 提供 SQLite 访问：连接管理、migration、repository 与事务。
//
// 技术架构 §6.3：单实例、WAL、foreign_keys 开启、busy_timeout 5000、
// 连接池最多 4 个；所有 SQL 参数化。驱动为纯 Go 的 modernc.org/sqlite，
// 因此二进制可以 CGO_ENABLED=0 构建并运行于 scratch 镜像。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"modernc.org/sqlite"

	"github.com/weddinggift/wedding/migrations"
)

// driverName 是 modernc.org/sqlite 注册的数据库驱动名。
const driverName = "sqlite"

// DB 包装 *sql.DB，承载 WeddingGift 的全部持久化操作。
type DB struct {
	db   *sql.DB
	path string
}

// Open 打开（必要时创建）SQLite 数据库并应用 §6.3 的 pragma 基线。
// 父目录不存在时以 0700 创建；数据库文件权限由进程 umask 决定，
// 部署侧由 ops 脚本保证目录 0700、属主容器用户。
func Open(ctx context.Context, path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建数据目录失败: %w", err)
		}
	}

	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=busy_timeout(5000)"

	sqldb, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// §6.3：单实例、连接池保持小规模。
	sqldb.SetMaxOpenConns(4)
	sqldb.SetMaxIdleConns(4)

	if err := sqldb.PingContext(ctx); err != nil {
		_ = sqldb.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	return &DB{db: sqldb, path: path}, nil
}

// Close 关闭数据库连接。
func (d *DB) Close() error {
	return d.db.Close()
}

// Path 返回数据库文件路径（仅用于日志与错误信息）。
func (d *DB) Path() string {
	return d.path
}

// SchemaVersion 返回当前 schema 版本（user_version pragma，0 表示未迁移）。
func (d *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := d.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("读取 schema 版本失败: %w", err)
	}
	return v, nil
}

// Migrate 应用全部待执行的 migration，返回本次应用的个数。
// 每个 migration 在独立事务内执行；数据库版本高于二进制自带版本时报错。
func (d *DB) Migrate(ctx context.Context) (int, error) {
	files, err := migrationFiles()
	if err != nil {
		return 0, err
	}

	current, err := d.SchemaVersion(ctx)
	if err != nil {
		return 0, err
	}
	if current > len(files) {
		return 0, fmt.Errorf("数据库 schema 版本 %d 高于当前二进制支持的 %d，请使用同版本或更新版本二进制", current, len(files))
	}

	applied := 0
	for i := current; i < len(files); i++ {
		body, err := fs.ReadFile(migrations.FS, files[i])
		if err != nil {
			return applied, fmt.Errorf("读取 migration %s 失败: %w", files[i], err)
		}
		if err := d.applyMigration(ctx, string(body), i+1); err != nil {
			return applied, fmt.Errorf("执行 migration %s 失败: %w", files[i], err)
		}
		applied++
	}
	return applied, nil
}

// applyMigration 在单个事务内执行一个 migration 文件并推进 user_version。
// 驱动支持多语句脚本，整个文件作为一次 Exec 提交。
func (d *DB) applyMigration(ctx context.Context, body string, version int) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	// user_version 只能通过常量赋值，不能参数化；version 来自文件名序号，非外部输入。
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}

// migrationFiles 返回按文件名排序的 migration 清单（NNNN_name.sql）。
func migrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("读取内嵌 migration 目录失败: %w", err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, errors.New("内嵌 migration 为空")
	}
	return files, nil
}

// RequireSchema 确认数据库已迁移到当前二进制期望的版本，否则提示先执行 migrate。
func (d *DB) RequireSchema(ctx context.Context) error {
	files, err := migrationFiles()
	if err != nil {
		return err
	}
	current, err := d.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if current != len(files) {
		return fmt.Errorf("数据库 schema 版本为 %d，期望 %d；请先执行 wedding migrate", current, len(files))
	}
	return nil
}

// IntegrityCheck 对当前数据库执行 PRAGMA integrity_check，结果非 ok 时返回错误。
func (d *DB) IntegrityCheck(ctx context.Context) error {
	return integrityCheck(ctx, d.db)
}

func integrityCheck(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("完整性检查执行失败: %w", err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("读取完整性检查结果失败: %w", err)
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(problems) > 0 {
		return fmt.Errorf("完整性检查未通过: %s", strings.Join(problems, "; "))
	}
	return nil
}

// BackupTo 使用 SQLite online backup API 把当前数据库一致性备份到 destPath。
// destPath 已存在时报错（备份文件不覆盖）；目标目录必须已存在。
// 备份完成后对产出文件执行完整性检查。
func (d *DB) BackupTo(ctx context.Context, destPath string) error {
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("备份目标已存在，拒绝覆盖: %s", destPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查备份目标失败: %w", err)
	}

	conn, err := d.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	err = conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("当前 SQLite 驱动不支持 online backup API")
		}
		b, err := backuper.NewBackup(destPath)
		if err != nil {
			return fmt.Errorf("初始化备份失败: %w", err)
		}
		for more := true; more; {
			more, err = b.Step(-1)
			if err != nil {
				_ = b.Finish()
				return fmt.Errorf("备份写入失败: %w", err)
			}
		}
		if err := b.Finish(); err != nil {
			return fmt.Errorf("备份收尾失败: %w", err)
		}
		return nil
	})
	if err != nil {
		_ = os.Remove(destPath)
		return err
	}

	// 对备份产物单独打开并做完整性检查（只读语义：不执行任何写操作）。
	// 备份产物继承源库的 WAL 模式，检查连接会留下 -wal/-shm sidecar，
	// 关闭连接已完成 checkpoint，sidecar 为空壳，显式删除保持产物为单一文件。
	check, err := sql.Open(driverName, "file:"+destPath)
	if err != nil {
		_ = os.Remove(destPath)
		return fmt.Errorf("打开备份产物失败: %w", err)
	}
	check.SetMaxOpenConns(1)
	err = integrityCheck(ctx, check)
	_ = check.Close()
	_ = os.Remove(destPath + "-wal")
	_ = os.Remove(destPath + "-shm")
	if err != nil {
		_ = os.Remove(destPath)
		return fmt.Errorf("备份产物校验失败: %w", err)
	}
	if err := os.Chmod(destPath, 0o600); err != nil {
		return fmt.Errorf("设置备份文件权限失败: %w", err)
	}
	return nil
}

// Ping 轻量探活（健康检查用）。
func (d *DB) Ping(ctx context.Context) error {
	return d.db.PingContext(ctx)
}

// ExecContext 执行原始 SQL。它是预留给集成测试与停服人工修正的底层入口；
// 正常业务路径必须使用本包的 repository 方法，不得绕过。
func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return d.db.ExecContext(ctx, query, args...)
}

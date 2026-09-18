package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var errMediaIndexLocked = errors.New("AC004.db 被相机原生服务占用")

type mediaIndexLocation struct {
	Storage  string
	Virtual  string
	Physical string
	Database string
}

func mediaIndexLocationFor(storage string) (mediaIndexLocation, bool) {
	switch storage {
	case "sd":
		return mediaIndexLocation{"sd", "/sd", "/mnt/media_rw/sd", "/mnt/media_rw/sd/MISC/AC004.db"}, true
	case "emulated":
		return mediaIndexLocation{"emulated", "/emulated", "/mnt/media_rw/emulated", "/mnt/media_rw/emulated/MISC/AC004.db"}, true
	default:
		return mediaIndexLocation{}, false
	}
}

func mediaIndexForPath(name string) (mediaIndexLocation, string, bool) {
	clean := path.Clean(name)
	if clean != name {
		return mediaIndexLocation{}, "", false
	}
	for _, storage := range []string{"sd", "emulated"} {
		location, _ := mediaIndexLocationFor(storage)
		if strings.HasPrefix(clean, location.Virtual+"/") {
			return location, location.Physical + "/" + strings.TrimPrefix(clean, location.Virtual+"/"), true
		}
	}
	return mediaIndexLocation{}, "", false
}

type MediaIndexStatus struct {
	Storage       string         `json:"storage"`
	Available     bool           `json:"available"`
	Database      string         `json:"database"`
	DatabaseBytes int64          `json:"database_bytes"`
	Total         int            `json:"total"`
	Stale         int            `json:"stale"`
	Invalid       int            `json:"invalid"`
	ByType        map[string]int `json:"by_type"`
	Reason        string         `json:"reason,omitempty"`
}

type MediaIndexCleanup struct {
	OK        bool   `json:"ok"`
	Storage   string `json:"storage"`
	Deleted   int    `json:"deleted"`
	Remaining int    `json:"remaining"`
	Backup    string `json:"backup,omitempty"`
}

func sqliteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func (s *fileStore) runSQLite(ctx context.Context, location mediaIndexLocation, jsonOutput bool, script string) ([]byte, error) {
	program, err := s.app.Tool("sqlite3")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	args := []string{"-batch"}
	if jsonOutput {
		args = append(args, "-json")
	}
	args = append(args, s.app.Path(location.Database))
	cmd := ownedCommand(ctx, program, args...)
	stdout, stderr := &boundedBuffer{limit: 8 << 20}, &boundedBuffer{limit: 64 << 10}
	cmd.Stdin = strings.NewReader(script)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err = cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.Text())
		if strings.Contains(message, "database is locked") {
			return nil, fmt.Errorf("%w: %s", errMediaIndexLocked, message)
		}
		return nil, fmt.Errorf("sqlite3: %w: %s", err, message)
	}
	if stdout.truncated {
		return nil, errors.New("sqlite3 output exceeds limit")
	}
	return []byte(stdout.Text()), nil
}

func (s *fileStore) mediaIndexReady(location mediaIndexLocation) (os.FileInfo, bool, error) {
	info, err := os.Lstat(s.app.Path(location.Database))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, errors.New("AC004.db is not a regular file")
	}
	if _, err = s.app.Tool("sqlite3"); err != nil {
		return info, false, nil
	}
	return info, true, nil
}

func (s *fileStore) indexedFile(location mediaIndexLocation, name string) (string, bool) {
	prefix := location.Physical + "/"
	if !strings.HasPrefix(name, prefix) {
		return "", false
	}
	relative := strings.TrimPrefix(name, prefix)
	if relative == "" || strings.ContainsAny(relative, "\\\x00") || path.Clean("/"+relative) != "/"+relative {
		return "", false
	}
	return filepath.Join(s.app.Path(location.Physical), filepath.FromSlash(relative)), true
}

func mediaIndexType(name string) string {
	ext := strings.TrimPrefix(strings.ToUpper(path.Ext(name)), ".")
	switch ext {
	case "MP4", "LRF", "JPG", "DNG":
		return ext
	default:
		return "OTHER"
	}
}

func (s *fileStore) scanMediaIndex(ctx context.Context, storage string) (MediaIndexStatus, []string, error) {
	location, ok := mediaIndexLocationFor(storage)
	if !ok {
		return MediaIndexStatus{}, nil, errors.New("unknown storage")
	}
	status := MediaIndexStatus{Storage: storage, Database: location.Virtual + "/MISC/AC004.db", ByType: map[string]int{"MP4": 0, "LRF": 0, "JPG": 0, "DNG": 0, "OTHER": 0}}
	info, ready, err := s.mediaIndexReady(location)
	if err != nil {
		return status, nil, err
	}
	if info == nil {
		status.Reason = "未找到 AC004.db"
		return status, nil, nil
	}
	status.DatabaseBytes = info.Size()
	if !ready {
		status.Reason = "设备缺少 sqlite3"
		return status, nil, nil
	}
	out, err := s.runSQLite(ctx, location, false, "PRAGMA query_only=ON; SELECT COUNT(*) FROM pragma_table_info('gis_info_table') WHERE name IN ('ID','file_name');\n")
	if err != nil {
		return status, nil, err
	}
	if strings.TrimSpace(string(out)) != "2" {
		return status, nil, errors.New("AC004.db 缺少 gis_info_table.ID/file_name")
	}
	out, err = s.runSQLite(ctx, location, false, "PRAGMA query_only=ON; PRAGMA quick_check;\n")
	if err != nil {
		return status, nil, err
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return status, nil, fmt.Errorf("AC004.db quick_check: %s", strings.TrimSpace(string(out)))
	}
	out, err = s.runSQLite(ctx, location, true, "PRAGMA query_only=ON; SELECT file_name FROM gis_info_table ORDER BY ID;\n")
	if err != nil {
		return status, nil, err
	}
	var rows []struct {
		FileName *string `json:"file_name"`
	}
	if len(strings.TrimSpace(string(out))) != 0 {
		if err = json.Unmarshal(out, &rows); err != nil {
			return status, nil, fmt.Errorf("decode AC004.db rows: %w", err)
		}
	}
	status.Available = true
	status.Total = len(rows)
	stale := make([]string, 0)
	for _, row := range rows {
		if row.FileName == nil {
			status.Invalid++
			continue
		}
		name, valid := s.indexedFile(location, *row.FileName)
		if !valid {
			status.Invalid++
			continue
		}
		entry, statErr := os.Lstat(name)
		if statErr == nil && entry.Mode().IsRegular() {
			continue
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return status, nil, statErr
		}
		status.Stale++
		status.ByType[mediaIndexType(*row.FileName)]++
		stale = append(stale, *row.FileName)
	}
	return status, stale, nil
}

func mediaIndexPredicate(name string, directory bool) string {
	quoted := sqliteLiteral(name)
	if !directory {
		return "file_name = " + quoted
	}
	return "file_name = " + quoted + " OR substr(file_name, 1, length(" + quoted + ") + 1) = " + quoted + " || '/'"
}

func (s *fileStore) mediaIndexContains(ctx context.Context, virtual string, directory bool) (bool, error) {
	location, name, ok := mediaIndexForPath(virtual)
	if !ok {
		return false, nil
	}
	_, ready, err := s.mediaIndexReady(location)
	if err != nil || !ready {
		return false, err
	}
	out, err := s.runSQLite(ctx, location, false, "PRAGMA query_only=ON; SELECT EXISTS(SELECT 1 FROM gis_info_table WHERE "+mediaIndexPredicate(name, directory)+");\n")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "1", nil
}

func (s *fileStore) removeMediaIndexPath(ctx context.Context, virtual string, directory bool) error {
	location, name, ok := mediaIndexForPath(virtual)
	if !ok {
		return nil
	}
	info, ready, err := s.mediaIndexReady(location)
	if err != nil || info == nil {
		return err
	}
	if !ready {
		return errors.New("文件已删除，但设备缺少 sqlite3，AC004.db 未同步")
	}
	_, err = s.runSQLite(ctx, location, false, ".timeout 5000\nBEGIN IMMEDIATE; DELETE FROM gis_info_table WHERE "+mediaIndexPredicate(name, directory)+"; COMMIT;\n")
	if err != nil {
		return fmt.Errorf("文件已删除，但 AC004.db 未同步: %w", err)
	}
	return nil
}

func (s *fileStore) cleanMediaIndex(ctx context.Context, storage string) (MediaIndexCleanup, error) {
	location, ok := mediaIndexLocationFor(storage)
	if !ok {
		return MediaIndexCleanup{}, errors.New("unknown storage")
	}
	status, stale, err := s.scanMediaIndex(ctx, storage)
	if err != nil {
		return MediaIndexCleanup{}, err
	}
	if !status.Available {
		return MediaIndexCleanup{}, errors.New(status.Reason)
	}
	if len(stale) == 0 {
		return MediaIndexCleanup{OK: true, Storage: storage}, nil
	}
	backupName := "AC004.db.bak_" + time.Now().Format("20060102_150405.000000000")
	backup := filepath.Join(filepath.Dir(s.app.Path(location.Database)), backupName)
	if _, err = s.runSQLite(ctx, location, false, ".backup "+strconv.Quote(backup)+"\n"); err != nil {
		return MediaIndexCleanup{}, fmt.Errorf("backup AC004.db: %w", err)
	}
	status, stale, err = s.scanMediaIndex(ctx, storage)
	if err != nil {
		return MediaIndexCleanup{}, err
	}
	backupVirtual := location.Virtual + "/MISC/" + backupName
	if len(stale) == 0 {
		return MediaIndexCleanup{OK: true, Storage: storage, Backup: backupVirtual}, nil
	}
	values := make([]string, len(stale))
	for i, name := range stale {
		values[i] = sqliteLiteral(name)
	}
	out, err := s.runSQLite(ctx, location, false, ".timeout 5000\nBEGIN IMMEDIATE; DELETE FROM gis_info_table WHERE file_name IN ("+strings.Join(values, ",")+"); SELECT changes(); COMMIT;\n")
	if err != nil {
		return MediaIndexCleanup{}, err
	}
	deleted, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return MediaIndexCleanup{}, fmt.Errorf("invalid sqlite3 change count: %w", err)
	}
	status, _, err = s.scanMediaIndex(ctx, storage)
	if err != nil {
		return MediaIndexCleanup{}, err
	}
	return MediaIndexCleanup{OK: true, Storage: storage, Deleted: deleted, Remaining: status.Stale, Backup: backupVirtual}, nil
}

func mediaIndexErrorCode(err error) (int, string, error) {
	if errors.Is(err, errMediaIndexLocked) {
		return 409, "media_index_locked", errors.New("数据库被相机原生服务占用；请先在系统页停止原生相机服务，再执行清理；完成后需要重启相机恢复原生功能")
	}
	return 500, "media_index_cleanup", err
}

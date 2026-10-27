package store

// 三本账（水位 / 统计 / 运行记录）在主库里的读写，以及旧 JSON 的只读导入。
//
// 导入只在 OpenDB 里做一次：对应表为空、且旧文件存在时搬进来。
// 旧文件此后只读保留，不再有任何写入者（I-082 验收标准 2，源码用例盯住）。

// loadWatermark 从主库装全部水位记录。
func (d *DB) loadWatermark() (map[string]Entry, error) {
	rows, err := d.handle.Query(`SELECT path, size, mod_time, fingerprint, failures, archive_path, processed_at FROM watermark`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := map[string]Entry{}
	for rows.Next() {
		var e Entry
		var modTime, processedAt string
		if err := rows.Scan(&e.Path, &e.Size, &modTime, &e.Fingerprint, &e.Failures, &e.ArchivePath, &processedAt); err != nil {
			return nil, err
		}
		e.ModTime = parseTime(modTime)
		e.ProcessedAt = parseTime(processedAt)
		entries[e.Path] = e
	}
	return entries, rows.Err()
}

// FlushWatermark 把给定的水位记录整表写进主库（测试与迁移工具用）。
func (d *DB) FlushWatermark(entries []Entry) error { return d.flushWatermark(entries) }

// flushWatermark 整表替换水位（一个事务）。水位不是 L1，替换是它正常的更新方式。
func (d *DB) flushWatermark(entries []Entry) error {
	return retryBusy(func() error { return d.flushWatermarkOnce(entries) })
}

func (d *DB) flushWatermarkOnce(entries []Entry) error {
	tx, err := d.handle.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM watermark`); err != nil {
		return err
	}
	for _, e := range entries {
		if _, err := tx.Exec(`INSERT INTO watermark
			(path, size, mod_time, fingerprint, failures, archive_path, processed_at)
			VALUES(?, ?, ?, ?, ?, ?, ?)`,
			e.Path, e.Size, fmtTime(e.ModTime), e.Fingerprint, e.Failures, e.ArchivePath, fmtTime(e.ProcessedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// loadCensus 从主库装全部统计缓存。
func (d *DB) loadCensus() (map[string]CensusEntry, error) {
	rows, err := d.handle.Query(`SELECT path, size, mod_time, state, checked_at FROM census`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	entries := map[string]CensusEntry{}
	for rows.Next() {
		var e CensusEntry
		var modTime, checkedAt string
		if err := rows.Scan(&e.Path, &e.Size, &modTime, &e.State, &checkedAt); err != nil {
			return nil, err
		}
		e.ModTime = parseTime(modTime)
		e.CheckedAt = parseTime(checkedAt)
		entries[e.Path] = e
	}
	return entries, rows.Err()
}

// flushCensus 整表替换统计缓存（一个事务）。
func (d *DB) flushCensus(entries []CensusEntry) error {
	return retryBusy(func() error { return d.flushCensusOnce(entries) })
}

func (d *DB) flushCensusOnce(entries []CensusEntry) error {
	tx, err := d.handle.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`DELETE FROM census`); err != nil {
		return err
	}
	for _, e := range entries {
		if _, err := tx.Exec(`INSERT INTO census (path, size, mod_time, state, checked_at)
			VALUES(?, ?, ?, ?, ?)`,
			e.Path, e.Size, fmtTime(e.ModTime), e.State, fmtTime(e.CheckedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// importLedgers 把旧 JSON 三件套搬进主库：只在对应表为空时做，重复打开零重复。
// 任何一边搬不动（文件读不出）都不挡打开——主库照常用，等于那本账从空开始。
func (d *DB) importLedgers() error {
	var n int
	if err := d.handle.QueryRow(`SELECT COUNT(*) FROM watermark`).Scan(&n); err != nil {
		return err
	}
	// 旧水位照搬进库：它不再是「要不要读这个文件」的依据（换代回填完成前由
	// Cursor 强制全读），但「已收集」这类数字还要靠它保住迁移前的样子。
	if n == 0 {
		if legacy, ok, err := readLegacyCursor(d.baseDir); err == nil && ok && len(legacy) > 0 {
			all := make([]Entry, 0, len(legacy))
			for _, e := range legacy {
				all = append(all, e)
			}
			if err := d.flushWatermark(all); err != nil {
				return err
			}
		}
	}

	if err := d.handle.QueryRow(`SELECT COUNT(*) FROM census`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if legacy, ok, err := readLegacyCensus(d.baseDir); err == nil && ok && len(legacy) > 0 {
			all := make([]CensusEntry, 0, len(legacy))
			for _, e := range legacy {
				all = append(all, e)
			}
			if err := d.flushCensus(all); err != nil {
				return err
			}
		}
	}

	if err := d.handle.QueryRow(`SELECT COUNT(*) FROM runs`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		if legacy, ok, err := readLegacyRuns(d.baseDir); err == nil && ok && len(legacy) > 0 {
			sortRuns(legacy)
			if err := d.importLegacyRuns(legacy); err != nil {
				return err
			}
		}
	}
	return nil
}

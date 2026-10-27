package store

// 视角表（facets）：L2a 的 LLM 支线（I-093）。
//
// 一行 = 一条模型给出的解读结论。缓存键 = 视角名 + 会话键 + prompt 版本 + 模型，
// 同样的问法第二次直接命中，不再花钱。表可以从头重建（整表删掉即可），
// 它不是真源（AC-14 / AC-15）。

import (
	"database/sql"
	"fmt"
)

// Facet 是一条视角结论。
type Facet struct {
	// Name 是视角名（topic / decision / todo）。
	Name string `json:"name"`
	// Key 是会话键：渠道|来源|会话标识。
	Key string `json:"key"`
	// Value 是模型返回的结构化结论（JSON 文本，原样存）。
	Value string `json:"value"`
	// Model / PromptVersion 用来解释「谁、按哪版提示词得出的」。
	Model         string `json:"model"`
	PromptVersion string `json:"prompt_version"`
	// CreatedAt 是生成时间（RFC3339Nano）。
	CreatedAt string `json:"created_at"`
}

// GetFacet 取一条缓存；没有时 ok = false。
func (d *DB) GetFacet(name, key, model, promptVersion string) (Facet, bool, error) {
	var f Facet
	err := d.handle.QueryRow(`SELECT name, key, value, model, prompt_version, created_at
		FROM facets WHERE name = ? AND key = ? AND model = ? AND prompt_version = ?`,
		name, key, model, promptVersion).
		Scan(&f.Name, &f.Key, &f.Value, &f.Model, &f.PromptVersion, &f.CreatedAt)
	if err == sql.ErrNoRows {
		return Facet{}, false, nil
	}
	if err != nil {
		return Facet{}, false, fmt.Errorf("读视角缓存: %w", err)
	}
	return f, true, nil
}

// PutFacet 写入或覆盖一条视角结论。
func (d *DB) PutFacet(f Facet) error {
	_, err := d.handle.Exec(`INSERT INTO facets(name, key, value, model, prompt_version, created_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(name, key, model, prompt_version) DO UPDATE SET
			value = excluded.value, created_at = excluded.created_at`,
		f.Name, f.Key, f.Value, f.Model, f.PromptVersion, f.CreatedAt)
	if err != nil {
		return fmt.Errorf("写视角缓存: %w", err)
	}
	return nil
}

// FacetNames 列出已有结论的视角名（诊断与查询入口用）。
func (d *DB) FacetNames() ([]string, error) {
	rows, err := d.handle.Query(`SELECT DISTINCT name FROM facets ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("列视角名: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

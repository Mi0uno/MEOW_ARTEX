package db

import "time"

// Wordbook 是一条用户上传的字典(子域爆破/目录路径/密码等)。
// 文件本体存 dataDir/wordbooks/<id>.txt,表里只存元数据;FileIndex 由上层维护。
type Wordbook struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	LineCount int       `json:"line_count"`
	ByteSize  int64     `json:"byte_size"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

// wordbookFileIndex 由上层(server)从 dataDir 推导,不入库。
const wordbookCols = `id, name, kind, line_count, byte_size, note, created_at`

func scanWordbook(sc interface{ Scan(...any) error }) (*Wordbook, error) {
	var w Wordbook
	if err := sc.Scan(&w.ID, &w.Name, &w.Kind, &w.LineCount, &w.ByteSize, &w.Note, &w.CreatedAt); err != nil {
		return nil, err
	}
	return &w, nil
}

// CreateWordbook 插入元数据行,返回 id(调用方先用 id 定文件名,写完文件再 Insert。
// 简化:调用方直接传全部字段一次性插入)。
func (d *DB) CreateWordbook(name, kind, filePath, note string, lineCount int, byteSize int64) (int64, error) {
	var id int64
	err := d.QueryRow(`
INSERT INTO wordbooks(name, kind, file_path, note, line_count, byte_size)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (name) DO UPDATE SET
  kind=EXCLUDED.kind, file_path=EXCLUDED.file_path, note=EXCLUDED.note,
  line_count=EXCLUDED.line_count, byte_size=EXCLUDED.byte_size, created_at=now()
RETURNING id`,
		name, kind, filePath, note, lineCount, byteSize).Scan(&id)
	return id, err
}

func (d *DB) ListWordbooks() ([]*Wordbook, error) {
	rows, err := d.Query(`SELECT ` + wordbookCols + ` FROM wordbooks ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Wordbook
	for rows.Next() {
		w, err := scanWordbook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (d *DB) GetWordbook(id int64) (*Wordbook, error) {
	w, err := scanWordbook(d.QueryRow(`SELECT `+wordbookCols+` FROM wordbooks WHERE id=$1`, id))
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (d *DB) DeleteWordbook(id int64) (bool, error) {
	res, err := d.Exec(`DELETE FROM wordbooks WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UpdateWordbookStats 写入文件落盘后的实际统计(行数/字节数/路径)。
func (d *DB) UpdateWordbookStats(id int64, byteSize int64, lineCount int, filePath string) error {
	_, err := d.Exec(`UPDATE wordbooks SET byte_size=$2, line_count=$3, file_path=$4 WHERE id=$1`,
		id, byteSize, lineCount, filePath)
	return err
}

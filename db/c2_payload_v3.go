package db

// C2 载荷矩阵 v3 访问层(契约 C2_CONTRACTS.md §19,Agent P 所有)。
// c2_payloads 三期新列:artifact(产物形态)/direction(连接方向)/staged(stage 模式)。
// 表结构见 schema.sql 三期段(CHECK 白名单已落),本层不改 schema。
//
// 为什么独立成文件:c2.go 的一期载荷面被 c2center.Store 适配器与既有 API 共用
// (Agent T 所有),三期只「追加」访问器,不动既有行——与本包 c2_evasion.go 的
// 二期回填列同款拆分纪律。
//
// 纪律(照 c2.go / c2_evasion.go):
//   - 枚举一律 Go 侧白名单校验(表上已有 CHECK,这里再兜一道);
//   - 复用 c2.go 的 scanC2Payload/c2PayloadCols,三期列在 V3 查询里并列追加,
//     保证一/二期调用方零改动;
//   - UpdateC2PayloadArtifact 回填与 UpdateC2PayloadBuildResult 同语义
//     (下载入口改指构建产物,sha256/size 换成产物的值)。

import (
	"database/sql"
	"fmt"
)

// 产物形态(c2_payloads.artifact,schema CHECK 白名单)。
const (
	C2PayloadArtifactScript    = "script"
	C2PayloadArtifactExe       = "exe"
	C2PayloadArtifactDll       = "dll"
	C2PayloadArtifactShellcode = "shellcode"
	C2PayloadArtifactEbpf      = "ebpf"
)

// ValidC2PayloadArtifact 校验产物形态白名单。
func ValidC2PayloadArtifact(a string) bool {
	switch a {
	case C2PayloadArtifactScript, C2PayloadArtifactExe, C2PayloadArtifactDll,
		C2PayloadArtifactShellcode, C2PayloadArtifactEbpf:
		return true
	}
	return false
}

// 连接方向(c2_payloads.direction)。
const (
	C2DirectionReverse = "reverse"
	C2DirectionBind    = "bind"
)

// ValidC2Direction 校验连接方向白名单。
func ValidC2Direction(d string) bool {
	return d == C2DirectionReverse || d == C2DirectionBind
}

// C2PayloadV3 是载荷行的一/二期字段 + 三期矩阵列(嵌入 C2Payload,序列化时
// json tag 原样带出,前端 C2Payload 类型已含 artifact/direction/staged)。
type C2PayloadV3 struct {
	C2Payload
	Artifact  string `json:"artifact"`  // script|exe|dll|shellcode|ebpf
	Direction string `json:"direction"` // reverse|bind
	Staged    bool   `json:"staged"`
}

const c2PayloadV3Cols = c2PayloadCols + `, p.artifact, p.direction, p.staged`

func scanC2PayloadV3(sc interface{ Scan(...any) error }) (*C2PayloadV3, error) {
	v := &C2PayloadV3{}
	if err := sc.Scan(&v.ID, &v.ListenerID, &v.Name, &v.Format, &v.Target, &v.Token,
		&v.FilePath, &v.SHA256, &v.Size, &v.Oneliner, &v.CreatedBy, &v.CreatedAt,
		&v.EvasionProfileID, &v.BuiltOnVPS,
		&v.Artifact, &v.Direction, &v.Staged); err != nil {
		return nil, err
	}
	return v, nil
}

// normalizeC2PayloadV3 填默认值并白名单校验三期三列(与 schema 默认一致:
// script/reverse/false)。
func normalizeC2PayloadV3(artifact, direction string, staged bool) (string, string, bool, error) {
	if artifact == "" {
		artifact = C2PayloadArtifactScript
	}
	if !ValidC2PayloadArtifact(artifact) {
		return "", "", false, fmt.Errorf("无效产物形态: %q(仅 script/exe/dll/shellcode/ebpf)", artifact)
	}
	if direction == "" {
		direction = C2DirectionReverse
	}
	if !ValidC2Direction(direction) {
		return "", "", false, fmt.Errorf("无效连接方向: %q(仅 reverse/bind)", direction)
	}
	return artifact, direction, staged, nil
}

// CreateC2PayloadV3 建载荷并带三期矩阵列(先走一期 CreateC2Payload 复用其校验与
// token 唯一约束,再单条 UPDATE 回填三列;两步非事务——失败最坏留下默认值行,
// 调用方可 DeleteC2Payload 回滚,与「先建行再落盘回填」的一期时序同风险级别)。
func (d *DB) CreateC2PayloadV3(in C2PayloadInput, artifact, direction string, staged bool) (int64, error) {
	artifact, direction, staged, err := normalizeC2PayloadV3(artifact, direction, staged)
	if err != nil {
		return 0, err
	}
	id, err := d.CreateC2Payload(in)
	if err != nil {
		return 0, err
	}
	if err := d.SetC2PayloadMatrix(id, artifact, direction, staged); err != nil {
		return id, err
	}
	return id, nil
}

// SetC2PayloadMatrix 回填/修正载荷的三期矩阵列(script 同步路径建行后补填用)。
func (d *DB) SetC2PayloadMatrix(id int64, artifact, direction string, staged bool) error {
	artifact, direction, staged, err := normalizeC2PayloadV3(artifact, direction, staged)
	if err != nil {
		return err
	}
	_, err = d.Exec(`UPDATE c2_payloads SET artifact=$2, direction=$3, staged=$4 WHERE id=$1`,
		id, artifact, direction, staged)
	return err
}

// UpdateC2PayloadArtifact 回填构建产物(契约 §19):file_path/sha256/size 换成构建
// 产物,built_on_vps=true,artifact 落列。构建失败不调用(job.error 记原因,载荷行
// 保持源码 zip 状态仍可下载)。
func (d *DB) UpdateC2PayloadArtifact(id int64, filePath, sha256 string, size int64, artifact string) error {
	if !ValidC2PayloadArtifact(artifact) {
		return fmt.Errorf("无效产物形态: %q", artifact)
	}
	_, err := d.Exec(`UPDATE c2_payloads SET
		file_path=$2, sha256=$3, size=$4, built_on_vps=true, artifact=$5
	WHERE id=$1`, id, filePath, sha256, size, artifact)
	return err
}

// ListC2PayloadsV3 列载荷(带三期列;listenerID 0 = 全部,新者在前)。
func (d *DB) ListC2PayloadsV3(listenerID int64) ([]*C2PayloadV3, error) {
	rows, err := d.Query(`SELECT `+c2PayloadV3Cols+` FROM c2_payloads p
WHERE ($1 = 0 OR p.listener_id = $1) ORDER BY p.id DESC`, listenerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*C2PayloadV3{}
	for rows.Next() {
		pl, err := scanC2PayloadV3(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, pl)
	}
	return out, rows.Err()
}

// GetC2PayloadV3 按数字 id 读载荷(带三期列;不存在返回 nil, nil)。
func (d *DB) GetC2PayloadV3(id int64) (*C2PayloadV3, error) {
	pl, err := scanC2PayloadV3(d.QueryRow(`SELECT `+c2PayloadV3Cols+` FROM c2_payloads p WHERE p.id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return pl, nil
}

// GetC2PayloadV3ByToken 按投递令牌读载荷(带三期列;stager 的 stage_get 平台侧
// 解析走这条:token → staged/artifact 白名单 → stage blob)。
func (d *DB) GetC2PayloadV3ByToken(token string) (*C2PayloadV3, error) {
	pl, err := scanC2PayloadV3(d.QueryRow(`SELECT `+c2PayloadV3Cols+` FROM c2_payloads p WHERE p.token=$1`, token))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return pl, nil
}

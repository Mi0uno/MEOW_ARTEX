package c2center

// 内存版 Store(fake):单测专用,不连库。实现语义与契约 §4 注释一致:
// UpsertSession 按 listener_id+beacon_uid 幂等(dead 不复活)、TakeQueuedTasks
// queued→sent 原子批量、UpdateTransfer 空 localPath 表示保持原值。

import (
	"errors"
	"sort"
	"sync"
	"time"
)

var errNotFound = errors.New("not found")

type fakeStore struct {
	mu        sync.Mutex
	listeners map[int64]*Listener
	keys      map[int64][]byte
	nextLID   int64

	sessions  map[int64]*Session
	sessBySID map[string]int64
	nextSess  int64

	tasks    map[int64]*Task
	nextTask int64

	transfers map[int64]*Transfer
	// localPaths 镜像 schema 的 local_path 列(冻结的 Transfer 结构体不含该字段,
	// 但 UpdateTransfer 契约要写入;测试断言用)。
	localPaths map[int64]string
	nextTf     int64

	loots    map[int64]*Loot
	nextLoot int64

	payloads map[int64]*Payload
	tokens   map[string]int64
	nextPay  int64
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		listeners:  map[int64]*Listener{},
		keys:       map[int64][]byte{},
		sessions:   map[int64]*Session{},
		sessBySID:  map[string]int64{},
		tasks:      map[int64]*Task{},
		transfers:  map[int64]*Transfer{},
		localPaths: map[int64]string{},
		loots:      map[int64]*Loot{},
		payloads:   map[int64]*Payload{},
		tokens:     map[string]int64{},
	}
}

// addListener 造一个监听器并写入密钥(测试装配)。
func (f *fakeStore) addListener(name, urlPath string, port int, enabled bool, key []byte) *Listener {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextLID++
	id := f.nextLID
	l := &Listener{
		ID: id, Name: name, Proto: "http", BindHost: "127.0.0.1", Port: port,
		URLPath: urlPath, Enabled: enabled,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	f.listeners[id] = l
	f.keys[id] = key
	return cloneListener(l)
}

func cloneListener(l *Listener) *Listener { c := *l; return &c }
func cloneSession(s *Session) *Session    { c := *s; return &c }
func cloneTask(t *Task) *Task             { c := *t; return &c }
func cloneTransfer(t *Transfer) *Transfer { c := *t; return &c }
func cloneLoot(l *Loot) *Loot             { c := *l; return &c }
func clonePayload(p *Payload) *Payload    { c := *p; return &c }

// setLastSeen 直改心跳时间(测试 sweep 用;GetSessionBySID 返回副本,直改不生效)。
func (f *fakeStore) setLastSeen(sid string, ts time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.sessBySID[sid]; ok {
		f.sessions[id].LastSeen = ts
	}
}

// transferLocalPath 读镜像的 local_path 列(Transfer 结构体冻结不含该字段)。
func (f *fakeStore) transferLocalPath(id int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.localPaths[id]
}

// ---- 监听器 ----

func (f *fakeStore) CreateListener(in ListenerInput) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextLID++
	id := f.nextLID
	l := &Listener{
		ID: id, Name: in.Name, Proto: "http", BindHost: in.BindHost, Port: in.Port,
		URLPath: in.URLPath, Callback: in.Callback, Notes: in.Notes,
		Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	f.listeners[id] = l
	return id, nil
}

func (f *fakeStore) ListListeners() ([]Listener, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Listener, 0)
	ids := make([]int64, 0, len(f.listeners))
	for id := range f.listeners {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		l := cloneListener(f.listeners[id])
		count := 0
		for _, s := range f.sessions {
			if s.ListenerID == id && s.Status == StatusOnline {
				count++
			}
		}
		l.SessionCount = count
		out = append(out, *l)
	}
	return out, nil
}

func (f *fakeStore) GetListener(id int64) (*Listener, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l, ok := f.listeners[id]; ok {
		return cloneListener(l), nil
	}
	return nil, nil
}

func (f *fakeStore) UpdateListener(id int64, in ListenerInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.listeners[id]
	if !ok {
		return errNotFound
	}
	l.Name, l.BindHost, l.Port, l.URLPath, l.Callback, l.Notes = in.Name, in.BindHost, in.Port, in.URLPath, in.Callback, in.Notes
	return nil
}

func (f *fakeStore) DeleteListener(id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.listeners, id)
	delete(f.keys, id)
	return nil
}

func (f *fakeStore) SetListenerEnabled(id int64, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.listeners[id]
	if !ok {
		return errNotFound
	}
	l.Enabled = enabled
	return nil
}

func (f *fakeStore) GetListenerKey(id int64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key, ok := f.keys[id]
	if !ok {
		return nil, errNotFound
	}
	return append([]byte(nil), key...), nil
}

// ---- 会话 ----

func (f *fakeStore) UpsertSession(in SessionInput) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, s := range f.sessions {
		if s.ListenerID == in.ListenerID && s.BeaconUID == in.BeaconUID {
			s.SID = in.SID
			s.Hostname, s.Username, s.Elevated = in.Hostname, in.Username, in.Elevated
			s.OS, s.Arch, s.LocalIPs, s.PID, s.Process = in.OS, in.Arch, in.LocalIPs, in.PID, in.Process
			if in.Transport != "" {
				s.Transport = in.Transport // 三期 §18:transport 标注(空=保持)
			}
			if in.Direction == "bind" && s.Meta != nil {
				s.Meta["direction"] = "bind" // 正向连接标注
			}
			delete(f.sessBySID, s.SID) // sid 换新令牌
			f.sessBySID[in.SID] = id
			if s.Status != StatusDead { // dead 不复活
				s.Status = StatusOnline
			}
			s.LastSeen = time.Now()
			return id, nil
		}
	}
	f.nextSess++
	id := f.nextSess
	now := time.Now()
	s := &Session{
		ID: id, ListenerID: in.ListenerID, BeaconUID: in.BeaconUID, SID: in.SID,
		Hostname: in.Hostname, Username: in.Username, Elevated: in.Elevated,
		OS: in.OS, Arch: in.Arch, LocalIPs: in.LocalIPs, PID: in.PID, Process: in.Process,
		SleepSec: defaultSleepSec, JitterPct: defaultJitterPct,
		Transport: transportFallback(in.Transport),
		Status:    StatusOnline, Meta: map[string]any{},
		FirstSeen: now, LastSeen: now,
	}
	if in.Direction == "bind" {
		s.Meta["direction"] = "bind"
	}
	f.sessions[id] = s
	f.sessBySID[in.SID] = id
	return id, nil
}

// transportFallback 空传输回落 http(与 db 层 COALESCE 口径一致)。
func transportFallback(t string) string {
	if t == "" {
		return ProtoHTTP
	}
	return t
}

func (f *fakeStore) GetSessionBySID(sid string) (*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.sessBySID[sid]; ok {
		return cloneSession(f.sessions[id]), nil
	}
	return nil, nil
}

func (f *fakeStore) GetSession(id int64) (*Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[id]; ok {
		return cloneSession(s), nil
	}
	return nil, nil
}

func (f *fakeStore) ListSessions(fl SessionFilter) ([]Session, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := make([]*Session, 0)
	for _, s := range f.sessions {
		if fl.Status != "" && s.Status != fl.Status {
			continue
		}
		if fl.ListenerID != 0 && s.ListenerID != fl.ListenerID {
			continue
		}
		if fl.TaskID != 0 && s.TaskID != fl.TaskID {
			continue
		}
		all = append(all, s)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	total := len(all)
	offset := fl.Offset
	if offset > len(all) {
		offset = len(all)
	}
	all = all[offset:]
	if fl.Limit > 0 && fl.Limit < len(all) {
		all = all[:fl.Limit]
	}
	out := make([]Session, 0, len(all))
	for _, s := range all {
		out = append(out, *cloneSession(s))
	}
	return out, total, nil
}

func (f *fakeStore) SetSessionSleep(sid string, sleepSec, jitterPct int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.sessBySID[sid]
	if !ok {
		return errNotFound
	}
	f.sessions[id].SleepSec = sleepSec
	f.sessions[id].JitterPct = jitterPct
	return nil
}

func (f *fakeStore) SetSessionStatusBySID(sid string, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.sessBySID[sid]
	if !ok {
		return errNotFound
	}
	f.sessions[id].Status = status
	return nil
}

func (f *fakeStore) BindSessionTask(id, taskID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return errNotFound
	}
	s.TaskID = taskID
	return nil
}

func (f *fakeStore) SweepOfflineSessions(graceMultiplier int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for _, s := range f.sessions {
		if s.Status != StatusOnline {
			continue
		}
		grace := time.Duration(s.SleepSec*(1+graceMultiplier)) * time.Second
		if time.Since(s.LastSeen) > grace {
			s.Status = StatusOffline
			n++
		}
	}
	return n, nil
}

// ---- 任务 ----

func (f *fakeStore) CreateTask(in TaskInput) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextTask++
	id := f.nextTask
	f.tasks[id] = &Task{
		ID: id, SessionID: in.SessionID, Kind: in.Kind, Input: in.Input,
		Status: TaskQueued, Actor: in.Actor, CreatedBy: in.CreatedBy,
		CreatedAt: time.Now(),
	}
	return id, nil
}

func (f *fakeStore) TakeQueuedTasks(sessionID int64, limit int) ([]Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]int64, 0)
	for id, t := range f.tasks {
		if t.SessionID == sessionID && t.Status == TaskQueued {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]Task, 0)
	for _, id := range ids {
		if limit > 0 && len(out) >= limit {
			break
		}
		t := f.tasks[id]
		now := time.Now()
		t.Status = TaskSent
		t.SentAt = &now
		out = append(out, *cloneTask(t))
	}
	return out, nil
}

func (f *fakeStore) FinishTask(id int64, status, output, outPath string, outSize int64, errText string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[id]
	if !ok {
		return errNotFound
	}
	t.Status = status
	t.Output = output
	t.OutputPath = outPath
	t.OutputSize = outSize
	t.Error = errText
	now := time.Now()
	t.DoneAt = &now
	return nil
}

func (f *fakeStore) GetTask(id int64) (*Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tasks[id]; ok {
		return cloneTask(t), nil
	}
	return nil, nil
}

func (f *fakeStore) ListTasks(sessionID, afterID int64, limit int) ([]Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]int64, 0)
	for id, t := range f.tasks {
		if t.SessionID == sessionID && id > afterID {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]Task, 0)
	for _, id := range ids {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, *cloneTask(f.tasks[id]))
	}
	return out, nil
}

func (f *fakeStore) CountQueuedTasks() (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, t := range f.tasks {
		if t.Status == TaskQueued {
			n++
		}
	}
	return n, nil
}

// ---- 文件传输 ----

func (f *fakeStore) CreateTransfer(in TransferInput) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextTf++
	id := f.nextTf
	f.transfers[id] = &Transfer{
		ID: id, SessionID: in.SessionID, TaskID: in.TaskID, Direction: in.Direction,
		RemotePath: in.RemotePath, Size: in.Size,
		Status: "queued", CreatedAt: time.Now(),
	}
	f.localPaths[id] = in.LocalPath
	return id, nil
}

func (f *fakeStore) UpdateTransfer(id int64, status string, doneSize, totalSize int64, localPath, errText string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.transfers[id]
	if !ok {
		return errNotFound
	}
	t.Status = status
	t.DoneSize = doneSize
	if totalSize >= 0 {
		t.Size = totalSize
	}
	if localPath != "" { // 空 = 保持原值(监听器跑动更新时不冲掉 server 建的路径)
		f.localPaths[id] = localPath
	}
	t.Error = errText
	if status == "done" {
		now := time.Now()
		t.DoneAt = &now
	}
	return nil
}

func (f *fakeStore) ListTransfers(sessionID int64, limit int) ([]Transfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]int64, 0)
	for id, t := range f.transfers {
		if t.SessionID == sessionID {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]Transfer, 0)
	for _, id := range ids {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, *cloneTransfer(f.transfers[id]))
	}
	return out, nil
}

func (f *fakeStore) GetTransfer(id int64) (*Transfer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.transfers[id]; ok {
		return cloneTransfer(t), nil
	}
	return nil, nil
}

// ---- 战利品 ----

func (f *fakeStore) CreateLoot(in LootInput) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextLoot++
	id := f.nextLoot
	f.loots[id] = &Loot{
		ID: id, SessionID: in.SessionID, TaskID: in.TaskID, Kind: in.Kind,
		Name: in.Name, Content: in.Content, BlobPath: in.BlobPath,
		SourcePath: in.SourcePath, CreatedAt: time.Now(),
	}
	return id, nil
}

func (f *fakeStore) ListLoot(fl LootFilter) ([]Loot, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := make([]*Loot, 0)
	for _, l := range f.loots {
		if fl.Kind != "" && l.Kind != fl.Kind {
			continue
		}
		if fl.SessionID != 0 && l.SessionID != fl.SessionID {
			continue
		}
		all = append(all, l)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	total := len(all)
	out := make([]Loot, 0, len(all))
	for _, l := range all {
		out = append(out, *cloneLoot(l))
	}
	return out, total, nil
}

func (f *fakeStore) GetLoot(id int64) (*Loot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l, ok := f.loots[id]; ok {
		return cloneLoot(l), nil
	}
	return nil, nil
}

func (f *fakeStore) DeleteLoot(id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.loots, id)
	return nil
}

// ---- 载荷 ----

func (f *fakeStore) CreatePayload(in PayloadInput) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextPay++
	id := f.nextPay
	f.payloads[id] = &Payload{
		ID: id, ListenerID: in.ListenerID, Name: in.Name, Format: in.Format,
		Target: in.Target, Token: in.Token, SHA256: in.SHA256, Size: in.Size,
		Oneliner: in.Oneliner, CreatedBy: in.CreatedBy, CreatedAt: time.Now(),
	}
	f.tokens[in.Token] = id
	return id, nil
}

func (f *fakeStore) ListPayloads(listenerID int64) ([]Payload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]int64, 0)
	for id, p := range f.payloads {
		if listenerID == 0 || p.ListenerID == listenerID {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]Payload, 0)
	for _, id := range ids {
		out = append(out, *clonePayload(f.payloads[id]))
	}
	return out, nil
}

func (f *fakeStore) GetPayload(id int64) (*Payload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.payloads[id]; ok {
		return clonePayload(p), nil
	}
	return nil, nil
}

func (f *fakeStore) GetPayloadByToken(token string) (*Payload, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.tokens[token]; ok {
		return clonePayload(f.payloads[id]), nil
	}
	return nil, nil
}

func (f *fakeStore) DeletePayload(id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.payloads[id]; ok {
		delete(f.tokens, p.Token)
	}
	delete(f.payloads, id)
	return nil
}

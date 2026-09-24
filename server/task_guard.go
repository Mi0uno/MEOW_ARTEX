package server

import (
	"log"
	"strconv"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// taskGuardResolver 实现 intercept.TaskGuardResolver:按 taskID 交出该任务的监管门限。
//
// 性能策略:只读内存任务句柄(Manager.tasks),不查库。
//
// 理由:这个回调挂在每次工具调用的入口(Match 与 Judge 各一次),任务跑起来后
// 每秒可能被调用几十次;若每次都查库,等于给每个工具调用加一次 PG 往返,还会在
// 数据库抖动时把监管判定拖成慢路径——而监管本身是个安全闸门,慢就等于被绕过。
// 内存里的门限由 taskFromPG(启动加载)与 UpdateTaskMetadata(PATCH 落库成功后)
// 两处写入,所以它与库里始终一致,不需要回读。
//
// 拿不到任务句柄时返回 ok=false 而不是 "off":这是安全控制的默认值方向问题。
// ok=false 让 intercept 侧按"跟随全局"处理(照常监管);若返回 "off",任何一次
// 加载竞态或 id 形态不匹配都会变成静默放通——放通是不可接受的失败模式,而不放通
// 只是行为保守。
//
// 已知取舍:任务句柄不存在于内存(例如刚建好但尚未 LoadExisting、或纯离线脚本
// 直接用 db 层)时不做库回退。此时按全局门限跑,是安全的保守方向。
func (s *Server) taskGuardResolver() intercept.TaskGuardResolver {
	return func(taskID string) (string, []int64, bool) {
		if s == nil || s.m == nil {
			return "", nil, false
		}
		t, ok := s.m.Task(taskID)
		if !ok || t == nil {
			return "", nil, false
		}
		mode, ruleIDs := t.taskGuardSnapshot()
		return mode, ruleIDs, true
	}
}

// wireTaskGuard 把任务级门限接进监管链路。必须在 New 里、wireInterceptReviewer
// 之后调用一次即可:resolver 是个闭包,读的是内存句柄,后续改门限无需重新注入。
func (s *Server) wireTaskGuard() {
	if s == nil || s.m == nil || s.m.interceptor == nil {
		return
	}
	s.m.interceptor.SetTaskGuardResolver(s.taskGuardResolver())
}

// validateGuardRuleIDs 校验任务级规则集限定里的 id 是否真实存在。
//
// 与 proxy_mode 指定代理组时校验组存在的理由一致:不校验的话,任务建好后会
// 因为一个不存在的 id 静默退化成"没有任何规则可匹配"(Match 里按 id 过滤后列表为空),
// 表现为监管开了但一条都不拦。这里在写库前就把错误暴露给调用方。
func (s *Server) validateGuardRuleIDs(ids []int64) error {
	if len(ids) == 0 || s == nil || s.m == nil || s.m.PG() == nil {
		return nil
	}
	rules, err := s.m.PG().ListInterceptRules()
	if err != nil {
		return err
	}
	known := make(map[int64]bool, len(rules))
	for _, r := range rules {
		known[r.ID] = true
	}
	for _, id := range ids {
		if !known[id] {
			return &unknownGuardRuleError{id: id}
		}
	}
	return nil
}

// unknownGuardRuleError 让 handler 能把"规则不存在"翻译成 400 而不是 500。
type unknownGuardRuleError struct{ id int64 }

func (e *unknownGuardRuleError) Error() string {
	return "监管规则 #" + strconv.FormatInt(e.id, 10) + " 不存在"
}

// taskGuardModeLabel 是门限取值的中文标签,用于日志与错误信息(前端有自己的一份,
// 这里只是让后端日志可读)。
func taskGuardModeLabel(mode string) string {
	switch db.NormalizeTaskGuardMode(mode) {
	case db.TaskGuardModeOff:
		return "本任务不跑监管"
	case db.TaskGuardModeEnforce:
		return "强制启用监管"
	default:
		return "跟随全局"
	}
}

// logTaskGuardChange 记录门限变更。监管是安全控制,谁在什么时候把某个任务的监管
// 关掉了必须留痕——这条日志是事后审计的入口(拦截审计行只记录实际发生的拦截,
// 记录不到"门限被改成 off 导致什么都没发生")。
func logTaskGuardChange(taskID, mode string, ruleIDs []int64) {
	log.Printf("[guard] 任务 #%s 监管门限改为 %s(%s),规则集限定 %d 条",
		taskID, taskGuardModeLabel(mode), mode, len(ruleIDs))
}

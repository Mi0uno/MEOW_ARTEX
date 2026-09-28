package server

// lockFinding 并发单测:同一 finding 的自动归类必须互斥(report_finding 钩子与
// update_finding_report 双触发,守卫先查后插的竞态曾让同一 finding 入库两条),
// 不同 finding 之间不互相阻塞。纯内存,不打 DB/LLM。

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestLockFindingSerializesSameFinding(t *testing.T) {
	s := &Server{}
	unlock := s.lockFinding(42)

	var entered atomic.Bool
	done := make(chan struct{})
	go func() {
		u := s.lockFinding(42)
		entered.Store(true)
		u()
		close(done)
	}()

	// 后到者必须阻塞:10ms 内不得进入临界区。
	time.Sleep(10 * time.Millisecond)
	if entered.Load() {
		t.Fatal("同 finding 的第二次 lockFinding 未被阻塞,互斥失效")
	}
	unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("释放锁后等待者仍未进入临界区")
	}
}

func TestLockFindingIndependentFindings(t *testing.T) {
	s := &Server{}
	unlock := s.lockFinding(42)
	defer unlock()

	acquired := make(chan func(), 1)
	go func() { acquired <- s.lockFinding(7) }()
	select {
	case u := <-acquired:
		u()
	case <-time.After(2 * time.Second):
		t.Fatal("不同 finding 的归类锁不应互斥")
	}
}

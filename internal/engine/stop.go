package engine

import "sync/atomic"

// StopEvent 是跨协程的取消信号（对应 Python threading.Event）。
type StopEvent struct {
	v atomic.Bool
}

// NewStopEvent 创建未触发的 StopEvent。
func NewStopEvent() *StopEvent { return &StopEvent{} }

// Set 置位。
func (s *StopEvent) Set() { s.v.Store(true) }

// IsSet 是否已置位。
func (s *StopEvent) IsSet() bool { return s.v.Load() }

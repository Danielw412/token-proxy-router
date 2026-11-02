package auth

// TPR: this file holds Token Proxy Router's hook into the auth manager. Keeping
// it in its own file keeps upstream merges cheap. See docs/upstream-sync.md.

// SchedulerFront is a scheduler that decides first and passes everything else on
// to whichever scheduler sits behind it.
type SchedulerFront interface {
	PluginScheduler
	SetNextScheduler(next PluginScheduler)
}

// SetSchedulerFront puts a scheduler in front of the current plugin scheduler. It
// stays in front when SetPluginScheduler later replaces what is behind it, which
// upstream does on every config reload.
func (m *Manager) SetSchedulerFront(front SchedulerFront) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.schedulerFront = front
	m.pluginScheduler = m.chainSchedulerLocked(m.pluginScheduler)
}

// chainSchedulerLocked returns the scheduler to install, given what upstream asked
// for. The caller holds m.mu.
func (m *Manager) chainSchedulerLocked(next PluginScheduler) PluginScheduler {
	if m.schedulerFront == nil {
		return next
	}
	if next == PluginScheduler(m.schedulerFront) {
		return next
	}
	m.schedulerFront.SetNextScheduler(next)
	return m.schedulerFront
}

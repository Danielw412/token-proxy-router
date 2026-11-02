package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type stubScheduler struct{ name string }

func (s *stubScheduler) PickAuth(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	return pluginapi.SchedulerPickResponse{}, false, nil
}

type stubFront struct {
	stubScheduler
	next PluginScheduler
}

func (f *stubFront) SetNextScheduler(next PluginScheduler) { f.next = next }

func installedScheduler(m *Manager) PluginScheduler {
	got, _ := m.snapshotPluginScheduler()
	return got
}

func TestSetPluginSchedulerIsUnchangedWithoutAFront(t *testing.T) {
	m := NewManager(nil, nil, nil)
	first := &stubScheduler{name: "first"}
	m.SetPluginScheduler(first)
	if got := installedScheduler(m); got != PluginScheduler(first) {
		t.Fatalf("installed = %v", got)
	}
	m.SetPluginScheduler(nil)
	if got := installedScheduler(m); got != nil {
		t.Fatalf("installed = %v, want nil", got)
	}
}

func TestSchedulerFrontStaysInFrontThroughConfigReloads(t *testing.T) {
	m := NewManager(nil, nil, nil)
	plugin := &stubScheduler{name: "plugin"}
	m.SetPluginScheduler(plugin)

	front := &stubFront{}
	m.SetSchedulerFront(front)
	if got := installedScheduler(m); got != PluginScheduler(front) {
		t.Fatalf("installed = %v, want the front", got)
	}
	if front.next != PluginScheduler(plugin) {
		t.Fatalf("the front must chain to the existing scheduler: %v", front.next)
	}

	// What upstream does on every config reload.
	replacement := &stubScheduler{name: "replacement"}
	m.SetPluginScheduler(replacement)
	if got := installedScheduler(m); got != PluginScheduler(front) {
		t.Fatalf("a reload displaced the front: %v", got)
	}
	if front.next != PluginScheduler(replacement) {
		t.Fatalf("the front must chain to the new scheduler: %v", front.next)
	}

	m.SetPluginScheduler(nil)
	if got := installedScheduler(m); got != PluginScheduler(front) {
		t.Fatalf("clearing the plugin displaced the front: %v", got)
	}
	if front.next != nil {
		t.Fatalf("next = %v, want nil", front.next)
	}

	m.SetPluginScheduler(front)
	if front.next != nil {
		t.Fatal("the front must not chain to itself")
	}
}

func TestSchedulerFrontAlsoAcceptsTheFirstInstall(t *testing.T) {
	m := NewManager(nil, nil, nil)
	front := &stubFront{}
	m.SetSchedulerFront(front)
	if front.next != nil {
		t.Fatalf("next = %v, want nil with nothing behind", front.next)
	}
	if !m.hasPluginScheduler() {
		t.Fatal("the manager must consult the front")
	}
}

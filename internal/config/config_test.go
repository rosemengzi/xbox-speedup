package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(filepath.Join(t.TempDir(), "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestInvalidConfigurationDoesNotReplaceLastGoodFile(t *testing.T) {
	cases := map[string]func(*Config){
		"cron": func(c *Config) { c.SpeedTest.Schedule = "not a cron" },
		"pin":  func(c *Config) { v := c.Platforms["XboxGlobal"]; v.PinnedIP = "bad"; c.Platforms["XboxGlobal"] = v },
		"cycle": func(c *Config) {
			c.Redirect.Rules = []RedirectRule{{From: "a.example", To: "b.example", Enabled: true}, {From: "b.example", To: "a.example", Enabled: true}}
		},
		"TLS conflict": func(c *Config) { c.Redirect.Enabled = true; c.WebTLS.Enabled = true; c.WebTLS.Addr = ":443" },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			m := newTestManager(t)
			before, _ := os.ReadFile(m.path)
			c := Clone(m.Get())
			edit(c)
			if err := m.Replace(c); err == nil {
				t.Fatal("invalid configuration accepted")
			}
			after, _ := os.ReadFile(m.path)
			if string(before) != string(after) {
				t.Fatal("invalid configuration replaced the last good file")
			}
		})
	}
}

func TestApplyFailureRollsBackMemoryDiskAndCallbacks(t *testing.T) {
	m := newTestManager(t)
	restored := false
	m.OnApply(func(c *Config) error {
		if c.Redirect.Enabled {
			return errors.New("port occupied")
		}
		restored = true
		return nil
	})
	c := Clone(m.Get())
	c.Redirect.Enabled = true
	if err := m.Replace(c); err == nil {
		t.Fatal("apply failure ignored")
	}
	if m.Get().Redirect.Enabled || !restored {
		t.Fatal("runtime configuration did not roll back")
	}
	loaded, err := load(m.path)
	if err != nil || loaded.Redirect.Enabled {
		t.Fatal("persisted configuration did not roll back")
	}
}

func TestConcurrentSavesKeepDiskMemoryAndCallbacksInOrder(t *testing.T) {
	m := newTestManager(t)
	lastPin := ""
	m.OnApply(func(c *Config) error { lastPin = c.Platforms["XboxGlobal"].PinnedIP; return nil })
	var wg sync.WaitGroup
	for i := 1; i <= 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := Clone(m.Get())
			v := c.Platforms["XboxGlobal"]
			v.PinnedIP = fmt.Sprintf("192.0.2.%d", i)
			c.Platforms["XboxGlobal"] = v
			if err := m.Replace(c); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	raw, err := os.ReadFile(m.path)
	if err != nil {
		t.Fatal(err)
	}
	var disk Config
	if err := yaml.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	want := m.Get().Platforms["XboxGlobal"].PinnedIP
	if disk.Platforms["XboxGlobal"].PinnedIP != want || lastPin != want {
		t.Fatal("disk, memory and callbacks disagree after concurrent saves")
	}
}

func TestReplaceDoesNotKeepCallerMapAliases(t *testing.T) {
	m := newTestManager(t)
	c := Default()
	if err := m.Replace(c); err != nil {
		t.Fatal(err)
	}
	c.Platforms["XboxGlobal"] = PlatformToggle{}
	if !m.Get().Platforms["XboxGlobal"].Enabled {
		t.Fatal("caller mutated live configuration after Replace")
	}
}

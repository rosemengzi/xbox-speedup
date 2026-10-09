package main

import (
	"testing"
	"xboxspeedup/internal/config"
	"xboxspeedup/internal/rules"
)

func TestIncrementalSpeedTestsRespectAutomaticAndPlatformSwitches(t *testing.T) {
	c := config.Default()
	table := &rules.Table{Platforms: map[string]rules.Platform{"XboxGlobal": {Pool: "Akamai"}, "Ps": {Pool: "Ps"}}}
	added := map[string][]string{"Akamai": {"192.0.2.1"}, "Ps": {"192.0.2.2"}}
	got := eligibleIncremental(c, table, added)
	if len(got) != 1 || len(got["Akamai"]) != 1 {
		t.Fatal("disabled platform participated in incremental test")
	}
	c.SpeedTest.Enabled = false
	if len(eligibleIncremental(c, table, added)) != 0 {
		t.Fatal("automatic speed switch ignored")
	}
	c.SpeedTest.Enabled = true
	toggle := c.Platforms["XboxGlobal"]
	toggle.PinnedIP = "192.0.2.10"
	c.Platforms["XboxGlobal"] = toggle
	if len(eligibleIncremental(c, table, added)) != 0 {
		t.Fatal("pinned-only pool still automatically tested")
	}
}

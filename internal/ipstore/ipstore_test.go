package ipstore

import (
	"testing"
	"time"
)

func TestComputeBestChoosesCurrentFastestDownload(t *testing.T) {
	p := newPool("test")
	p.SetList([]IPEntry{
		{IP: "192.0.2.1", Location: "slow"},
		{IP: "192.0.2.2", Location: "fast"},
	})
	p.UpdatePing("192.0.2.1", 5)
	p.UpdatePing("192.0.2.2", 10)
	p.UpdateSpeed("192.0.2.1", 1.92)
	p.UpdateSpeed("192.0.2.2", 10.46)

	if got := p.ComputeBest(); got != "192.0.2.2" {
		t.Fatalf("ComputeBest() = %q, want current fastest IP %q", got, "192.0.2.2")
	}
}

func TestRetainSpeedsInvalidatesIPsOutsideCurrentTopN(t *testing.T) {
	p := newPool("test")
	p.SetList([]IPEntry{{IP: "192.0.2.1"}, {IP: "192.0.2.2"}})
	p.UpdateSpeed("192.0.2.1", 20)
	p.UpdateSpeed("192.0.2.2", 10)

	p.RetainSpeeds([]string{"192.0.2.2"})

	if got := p.records["192.0.2.1"].SpeedMBps; got != -1 {
		t.Fatalf("invalidated speed = %v, want -1", got)
	}
	if !p.records["192.0.2.1"].TestedAt.IsZero() {
		t.Fatal("invalidated record still has a test timestamp")
	}
	if got := p.records["192.0.2.2"].SpeedMBps; got != 10 {
		t.Fatalf("retained speed = %v, want 10", got)
	}
}

func TestPingOnlyPoolChoosesLowestRTT(t *testing.T) {
	p := newPool("test")
	p.SetList([]IPEntry{{IP: "192.0.2.1"}, {IP: "192.0.2.2"}})
	p.UpdatePing("192.0.2.1", 20)
	p.UpdatePing("192.0.2.2", 5)

	if got := p.ComputeBestByLatency(); got != "192.0.2.2" {
		t.Fatalf("ComputeBest() = %q, want lowest RTT IP %q", got, "192.0.2.2")
	}
}

func TestUnmeasuredAndFailedDownloadsHaveNoBest(t *testing.T) {
	p := newPool("download")
	p.SetList([]IPEntry{{IP: "192.0.2.1"}, {IP: "192.0.2.2"}})
	if got := p.Best(); got != "" {
		t.Fatalf("unmeasured best = %q", got)
	}
	for _, ip := range p.IPs() {
		p.UpdatePing(ip, 5)
		p.UpdateSpeed(ip, 0)
	}
	if got := p.ComputeBest(); got != "" || p.Best() != "" {
		t.Fatalf("failed downloads selected %q", got)
	}
}

func TestFailedChampionIsInvalidatedAndRetried(t *testing.T) {
	p := newPool("download")
	p.SetList([]IPEntry{{IP: "192.0.2.1"}})
	p.UpdateSpeed("192.0.2.1", 10)
	p.ComputeBest()
	p.UpdateSpeed("192.0.2.1", 0)
	if p.Best() != "" || p.Fresh("192.0.2.1", 30*time.Minute) {
		t.Fatal("failed champion is still served or skipped as fresh")
	}
}

func TestSetListRejectsInvalidAndDuplicateIPs(t *testing.T) {
	p := newPool("test")
	added := p.SetList([]IPEntry{{IP: "bad"}, {IP: "::1"}, {IP: "0.0.0.0"}, {IP: "192.0.2.1"}, {IP: "192.0.2.1"}})
	if len(added) != 1 || len(p.IPs()) != 1 {
		t.Fatalf("invalid/duplicate candidates retained: %v", p.IPs())
	}
}

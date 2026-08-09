package ipstore

import "testing"

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

func TestComputeBestFallsBackToLowestRTT(t *testing.T) {
	p := newPool("test")
	p.SetList([]IPEntry{{IP: "192.0.2.1"}, {IP: "192.0.2.2"}})
	p.UpdatePing("192.0.2.1", 20)
	p.UpdatePing("192.0.2.2", 5)

	if got := p.ComputeBest(); got != "192.0.2.2" {
		t.Fatalf("ComputeBest() = %q, want lowest RTT IP %q", got, "192.0.2.2")
	}
}

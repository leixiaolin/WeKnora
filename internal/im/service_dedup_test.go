package im

import "testing"

func TestMakeMessageDedupIDIncludesChannel(t *testing.T) {
	if got := makeMessageDedupID("channel-1", "msg-1"); got != "channel-1:msg-1" {
		t.Fatalf("dedup id = %q", got)
	}
	if makeMessageDedupID("channel-a", "same") == makeMessageDedupID("channel-b", "same") {
		t.Fatal("dedup id must differ across channels")
	}
}

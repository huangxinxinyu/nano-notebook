package agent

import "testing"

func TestSourceGroundingPlanSHA256CanonicalizesEmptyReferences(t *testing.T) {
	nilHash, err := sourceGroundingPlanSHA256("Source-less answer.", nil)
	if err != nil {
		t.Fatal(err)
	}
	emptyHash, err := sourceGroundingPlanSHA256("Source-less answer.", []string{})
	if err != nil {
		t.Fatal(err)
	}
	if nilHash != emptyHash {
		t.Fatalf("empty reference hashes differ: nil=%s allocated=%s", nilHash, emptyHash)
	}
	withReference, err := sourceGroundingPlanSHA256("Source-less answer.", []string{"src_a"})
	if err != nil {
		t.Fatal(err)
	}
	if withReference == nilHash {
		t.Fatalf("non-empty references unexpectedly share the source-less hash: %s", withReference)
	}
}

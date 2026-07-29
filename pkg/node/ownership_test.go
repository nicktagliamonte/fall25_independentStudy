package node

import "testing"

func TestOwnerElectionCandidateMinimum(t *testing.T) {
	for _, test := range []struct {
		cluster int
		want    int
	}{
		{cluster: 0, want: 0},
		{cluster: 1, want: 0},
		{cluster: 3, want: 2},
		{cluster: 10, want: 9},
		{cluster: 50, want: 16},
	} {
		if got := ownerElectionCandidateMinimum(test.cluster); got != test.want {
			t.Fatalf("cluster %d: minimum = %d, want %d", test.cluster, got, test.want)
		}
	}
}

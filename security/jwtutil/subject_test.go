package jwtutil

import "testing"

func TestNormalizeSubjectID(t *testing.T) {
	uuid := "11111111-1111-1111-1111-111111111111"
	cases := []struct {
		in      string
		want    string
		wantOK  bool
	}{
		{uuid, uuid, true},
		{SubjectPrefixUser + uuid, uuid, true},
		{"usr_not-a-uuid", "", false},
		{"not-a-uuid", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := NormalizeSubjectID(tc.in)
		if ok != tc.wantOK || got != tc.want {
			t.Fatalf("NormalizeSubjectID(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}
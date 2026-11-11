package gmaps

import "testing"

func TestCidFromDataID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		dataID string
		want   string
	}{
		{"valid", "0x14a1a32f316b15a1:0x169c54b46dcc3a93", "1629270299114224275"},
		{"uppercase hex", "0x14A1A32F316B15A1:0x169C54B46DCC3A93", "1629270299114224275"},
		{"0X prefix on both halves", "0X14a1a32f316b15a1:0X169c54b46dcc3a93", "1629270299114224275"},
		{"max uint64", "0x1:0xffffffffffffffff", "18446744073709551615"},
		{"overflow", "0x1:0x1ffffffffffffffff", ""},
		{"no colon", "0x14a1a32f316b15a1", ""},
		{"empty", "", ""},
		{"bad hex", "0x1:0xzz", ""},
		{"only colon", ":", ""},
		{"empty hex", "0x1:", ""},
		{"prefix only", "0x1:0x", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := cidFromDataID(tc.dataID); got != tc.want {
				t.Fatalf("cidFromDataID(%q) = %q, want %q", tc.dataID, got, tc.want)
			}
		})
	}
}

package platform

import "testing"

// Fixed vectors preserve the Lark v1 canonical JSON, including HTML escaping,
// exact UTF-8/CRLF text and ordered image bytes.
func TestImageContentDigestCompatibility(t *testing.T) {
	for _, tc := range []struct {
		body   string
		images [][]byte
		want   string
	}{
		{"", nil, "5afc32163fbf070c02f5c14d2d8cd425cd65ae4f04a978aece5233e258ced900"},
		{"中文\r\n<&>", [][]byte{{0, 1, 2}, {3, 4}}, "d424fb5d3d815a8e1e4d3ee74c7cac04ec2e8d9ced6d80ba6439c26785017d8c"},
		{"exact body", [][]byte{{0, 1, 2}}, "b24f4a3aea46fe72e53818bdb861c57a8baafdc3982c29733aa225dea0be0da5"},
	} {
		if got := ImageContentDigest(tc.body, tc.images); got != tc.want {
			t.Fatal("v1 digest changed", got, tc.want)
		}
	}
}

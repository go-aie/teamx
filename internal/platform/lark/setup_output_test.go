package lark

import (
	"context"
	"strings"
	"testing"
)

func TestRegistrationURLAndFragmentedOutput(t *testing.T) {
	link := "https://open.feishu.cn/page/cli?user_code=ABC-123&lpv=1.0.99&ocv=1.0.99&from=cli"
	for _, raw := range []string{
		strings.Replace(link, "open.feishu.cn", "open.feishu.cn.evil", 1),
		strings.Replace(link, "/page/cli", "/page/launcher", 1),
		strings.Replace(link, "https://", "http://", 1),
		link + "&from=cli", link + "#fragment", link + "&tp=other",
	} {
		if validRegistrationURL(raw, "feishu") {
			t.Fatal("unsafe or rewritten link accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got string
	out := &registrationOutput{buffer: limitedBuffer{limit: 64 << 10, cancel: cancel}, brand: "feishu", cancel: cancel, ready: func(raw string) error { got = raw; return nil }}
	for _, chunk := range []string{"QR\n  https://open.", link[len("https://open."):], "\r", "\nwaiting\n"} {
		if _, err := out.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	if got != link || ctx.Err() != nil {
		t.Fatal("fragmented URL was changed or lost")
	}
}

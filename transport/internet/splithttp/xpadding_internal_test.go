package splithttp

import (
	"net/url"
	"testing"
)

func TestRawQueryValueMatchesParseQuery(t *testing.T) {
	for _, q := range []string{
		"",
		"x_padding=XXXX",
		"a=1&x_padding=XXXX&x_padding=YY",
		"x_padding",
		"x_padding=",
		"x%5Fpadding=a+b%21",
		"x_padding=%zz&x_padding=ok",
		"x_padding=a;b&x_padding=c",
		"&&x_padding=v&",
		"y=1",
	} {
		values, _ := url.ParseQuery(q)
		if got, want := rawQueryValue(q, "x_padding"), values.Get("x_padding"); got != want {
			t.Errorf("%q: got %q, want %q", q, got, want)
		}
	}
}

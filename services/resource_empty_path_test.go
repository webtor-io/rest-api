package services

import (
	"reflect"
	"testing"
)

func TestDropEmptyComponents(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"a.mp3"}, []string{"a.mp3"}},
		{[]string{"", "a.mp3"}, []string{"a.mp3"}},
		{[]string{"sub", "", "a.mp3"}, []string{"sub", "a.mp3"}},
		{[]string{"", "", "a.mp3"}, []string{"a.mp3"}},
		{[]string{"sub", "a.mp3"}, []string{"sub", "a.mp3"}},
	}
	for _, c := range cases {
		if got := dropEmptyComponents(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("dropEmptyComponents(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

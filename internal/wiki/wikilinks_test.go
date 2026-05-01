package wiki

import (
	"reflect"
	"sort"
	"testing"
)

func TestParseWikilinks(t *testing.T) {
	tests := []struct {
		name, in string
		want     []Wikilink
	}{
		{
			"basic",
			"See [[voyage-context-3]] for details.",
			[]Wikilink{{Target: "voyage-context-3", Label: ""}},
		},
		{
			"with_label",
			"See [[wiki://main/entities/foo.md|foo]] now.",
			[]Wikilink{{Target: "wiki://main/entities/foo.md", Label: "foo"}},
		},
		{
			"multiple",
			"[[a]] and [[b|alias]] and [[c]].",
			[]Wikilink{
				{Target: "a", Label: ""},
				{Target: "b", Label: "alias"},
				{Target: "c", Label: ""},
			},
		},
		{
			"inside_code_fence_ignored",
			"Use ```\n[[not_a_link]]\n``` for that.",
			nil,
		},
		{
			"inline_code_ignored",
			"Use `[[not_a_link]]` carefully.",
			nil,
		},
		{
			"escaped_brackets_ignored",
			`See \[\[not\_a\_link\]\] documented.`,
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseWikilinks(tt.in)
			sort.Slice(got, func(i, j int) bool { return got[i].Target < got[j].Target })
			sort.Slice(tt.want, func(i, j int) bool { return tt.want[i].Target < tt.want[j].Target })
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

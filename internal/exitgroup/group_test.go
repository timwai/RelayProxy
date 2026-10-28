package exitgroup

import "testing"

func TestOrderedCandidatesAndValidation(t *testing.T) {
	g := []Group{{ID: "group:wan", Name: "主备出口", Members: []string{"a", "b", "c"}}}
	if e := Validate(g, "group:wan"); e != nil {
		t.Fatal(e)
	}
	if e := Validate(g, "group:missing"); e == nil {
		t.Fatal("dangling reference accepted")
	}
	c := Candidates(g, "group:wan", func(id string) bool { return id != "b" })
	if len(c) != 2 || c[0] != "a" || c[1] != "c" {
		t.Fatal(c)
	}
	c[0] = "changed"
	if g[0].Members[0] != "a" {
		t.Fatal("shared storage")
	}
	for _, members := range [][]string{nil, {"a", "a"}, {"group:nested"}, {"local:socks"}, {""}} {
		bad := Clone(g)
		bad[0].Members = members
		if Validate(bad) == nil {
			t.Fatalf("accepted %v", members)
		}
	}
	renamed := Clone(g)
	renamed[0].Name = "改名"
	if Validate(renamed, "group:wan") != nil {
		t.Fatal("rename changed identity")
	}
}

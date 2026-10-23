package selfupdate

import "testing"

func TestParseNotes(t *testing.T) {
	body := "## Breaking\n\n- drop z (#4)\n\n## New\n\n- add y (#2)\n- add w\n\n## Fixes\n\n- mend x (#3)\n\nBuilt from abc1234. On the device, update from the companion.\n"
	c := ParseNotes(body)
	if c == nil || c.Breaking != 1 || c.New != 2 || c.Fixes != 1 {
		t.Fatalf("counts = %+v", c)
	}
	want := "## Breaking\n\n- drop z (#4)\n\n## New\n\n- add y (#2)\n- add w\n\n## Fixes\n\n- mend x (#3)"
	if c.Notes != want {
		t.Fatalf("notes = %q", c.Notes)
	}
	if c := ParseNotes("## Fixes\r\n\r\n- mend x\r\n"); c == nil || c.Fixes != 1 || c.Notes != "## Fixes\n\n- mend x" {
		t.Fatalf("crlf = %+v", c)
	}
	for _, none := range []string{"", "Built from abc1234.", "## Other\n\n- x\n"} {
		if c := ParseNotes(none); c != nil {
			t.Errorf("%q gave %+v", none, c)
		}
	}
}

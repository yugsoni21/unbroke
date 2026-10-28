package catalog

import "testing"

func TestHints(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	stripe, _ := c.Find("stripe")
	if h := stripe.Hints(); len(h) != 1 || h[0] != "stripe" {
		t.Errorf("stripe hints = %v", h)
	}
	gh, _ := c.Find("github")
	for _, h := range gh.Hints() {
		if h == "github" {
			t.Error(`"github" alone appears in nearly every repository; detect GitHub through its libraries and API host`)
		}
	}
	for _, p := range c.Providers {
		if len(p.Hints()) == 0 {
			t.Errorf("%s has no hints", p.ID)
		}
		for _, h := range p.Hints() {
			if len(h) < 3 {
				t.Errorf("%s hint %q is too short to be a reliable signal", p.ID, h)
			}
		}
	}
}

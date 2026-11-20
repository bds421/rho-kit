package pgtemplate

import (
	"context"
	"testing"
)

func noBuild(context.Context, string) error { return nil }

func TestATemplateWithoutItsPartsIsRefused(t *testing.T) {
	for _, tp := range []Template{{Hash: "h", Build: noBuild}, {Prefix: "p_", Build: noBuild}, {Prefix: "p_", Hash: "h"}} {
		if err := tp.Clone(t.Context(), nil, "copy"); err == nil {
			t.Errorf("%+v cloned", tp)
		}
	}
}

func TestACopyUnderTheTemplatesPrefixIsRefused(t *testing.T) {
	tp := Template{Prefix: "tmpl_", Hash: "h", Build: noBuild}
	if err := tp.Clone(t.Context(), nil, "tmpl_copy"); err == nil {
		t.Fatal("cloned into a name a later build could drop")
	}
}

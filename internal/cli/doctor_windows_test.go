package cli

import (
	"slices"
	"testing"
)

func TestSourceFormsSpellAWindowsPathWithEitherSlash(t *testing.T) {
	forms := sourceForms(`C:\Users\you\app`)
	for _, want := range []string{`C:\Users\you\app`, `C:/Users/you/app`, `C:\\Users\\you\\app`, `C:\/Users\/you\/app`} {
		if !slices.Contains(forms, want) {
			t.Errorf("forms %q lack %q", forms, want)
		}
	}
}

package names

import "testing"

func TestWithinMatchesWholeLabelsIgnoringCaseAndATrailingDot(t *testing.T) {
	for _, c := range []struct {
		name string
		want bool
	}{
		{"feat-x.myapp.localhost", true},
		{"app.feat-x.myapp.localhost", true},
		{"FEAT-X.MyApp.localhost", true},
		{"feat-x.myapp.localhost.", true},
		{"xfeat-x.myapp.localhost", false},
		{"myapp.localhost", false},
		{"feat-x.myapp.localhost.evil.test", false},
		{"", false},
	} {
		if got := Within("Feat-X.myapp.localhost.", c.name); got != c.want {
			t.Errorf("Within(feat-x.myapp.localhost, %q) = %v, want %v", c.name, got, c.want)
		}
	}
}

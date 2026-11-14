package proxy

import "testing"

var featX = Berth{Name: "feat-x", Host: "feat-x.myapp.localhost", Port: Target{Port: 20001},
	Routes: map[string]Target{"vite.feat-x.myapp.localhost": {Port: 20002}}}

func TestLookupMatchesTheHostAndNamesUnderItWhateverTheCasePortOrTrailingDot(t *testing.T) {
	table := Table{featX}
	for _, host := range []string{"feat-x.myapp.localhost", "app.feat-x.myapp.localhost", "FEAT-X.MyApp.localhost.", "app.feat-x.myapp.localhost:8080"} {
		b, target, ok := table.Lookup(host)
		if !ok || b.Name != "feat-x" || target.Port != 20001 {
			t.Errorf("Lookup(%q) = %s port %d, %v; want feat-x on 20001", host, b.Name, target.Port, ok)
		}
	}
}

func TestLookupFindsNoBerthForANameThatOnlyEndsWithItsHost(t *testing.T) {
	table := Table{featX}
	for _, host := range []string{"myfeat-x.myapp.localhost", "x.myapp.localhost", "myapp.localhost", "feat-x.myapp.localhost.evil.example", ""} {
		if b, _, ok := table.Lookup(host); ok {
			t.Errorf("Lookup(%q) found %s, want no berth", host, b.Name)
		}
	}
}

func TestLookupSendsARoutedNameToItsOwnPort(t *testing.T) {
	table := Table{featX}
	if _, target, _ := table.Lookup("Vite.feat-x.myapp.localhost:80"); target.Port != 20002 {
		t.Errorf("the routed name went to port %d, want 20002", target.Port)
	}
	if _, target, _ := table.Lookup("app.vite.feat-x.myapp.localhost"); target.Port != 20001 {
		t.Errorf("a name under the routed one went to port %d, want the berth's port 20001", target.Port)
	}
}

func TestLookupGivesANameToTheMostSpecificOfNestedHosts(t *testing.T) {
	inner := Berth{Name: "app", Host: "app.feat-x.myapp.localhost", Port: Target{Port: 20003}}
	for _, table := range []Table{{featX, inner}, {inner, featX}} {
		if b, _, _ := table.Lookup("api.app.feat-x.myapp.localhost"); b.Name != "app" {
			t.Errorf("api.app.feat-x.myapp.localhost went to %s, want the berth whose host it is directly under", b.Name)
		}
		if b, _, _ := table.Lookup("feat-x.myapp.localhost"); b.Name != "feat-x" {
			t.Errorf("feat-x.myapp.localhost went to %s, want feat-x", b.Name)
		}
	}
}

package mysql

import "testing"

// Rails and other MySQL URL readers merge database from the query over the path.
func TestURLWhoseQueryNamesTheDatabaseIsRefused(t *testing.T) {
	env := map[string]string{"DATABASE_URL": "mysql://u:p@h:3306/app?database=app"}
	if _, _, err := Engine.Location("", "DATABASE_URL").Server(env); err == nil {
		t.Error("a URL whose query sets database was accepted")
	}
}

package schema

import (
	"io"
	"strconv"
	"strings"

	"github.com/standards-lab/sqlate/migrate"

	"github.com/JaimeStill/spike-s3-storage/app/output"
)

// writeStatus writes sets as status prints them: one row per set under
// set, table, version, latest, pending, and dirty, the pending column
// listing the pending migrations as "N name", or none.
func writeStatus(w io.Writer, sets []migrate.SetStatus) error {
	rows := make([][]string, 0, len(sets))
	for _, s := range sets {
		pending := "none"
		if len(s.Pending) > 0 {
			names := make([]string, 0, len(s.Pending))
			for _, m := range s.Pending {
				names = append(names, strconv.Itoa(m.Version)+" "+m.Name)
			}
			pending = strings.Join(names, ", ")
		}
		rows = append(rows, []string{
			s.Name, s.Table, strconv.Itoa(s.Version), strconv.Itoa(s.Latest), pending, strconv.FormatBool(s.Dirty),
		})
	}
	return output.Table(w, []string{"set", "table", "version", "latest", "pending", "dirty"}, rows)
}

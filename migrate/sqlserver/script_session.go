package sqlserver

import (
	"fmt"
	"regexp"
	"strings"
)

// SessionOptions are the SET options every migration statement runs under,
// whether the executor applies an artifact or a rendered script does.
const SessionOptions = "SET XACT_ABORT ON; SET ANSI_NULLS ON; SET ANSI_PADDING ON; SET ANSI_WARNINGS ON; SET ARITHABORT ON; SET CONCAT_NULL_YIELDS_NULL ON; SET QUOTED_IDENTIFIER ON; SET NUMERIC_ROUNDABORT OFF;"

// restoreSessionOptions puts back, after the commit, each runtime option that
// SessionOptions changed, from the @@OPTIONS bits saved before it. SQL Server
// applies SET QUOTED_IDENTIFIER while it parses the batch, so a conditional SET
// cannot restore it; it stays ON. An error aborts the batch before this runs.
const restoreSessionOptions = "IF @options & 16384 = 0 SET XACT_ABORT OFF;\n" +
	"IF @options & 32 = 0 SET ANSI_NULLS OFF;\n" +
	"IF @options & 16 = 0 SET ANSI_PADDING OFF;\n" +
	"IF @options & 8 = 0 SET ANSI_WARNINGS OFF;\n" +
	"IF @options & 64 = 0 SET ARITHABORT OFF;\n" +
	"IF @options & 4096 = 0 SET CONCAT_NULL_YIELDS_NULL OFF;\n" +
	"IF @options & 8192 <> 0 SET NUMERIC_ROUNDABORT ON;\n"

// LockResource is the applock that serialises every migration of a database,
// applied directly or through a rendered script.
const LockResource = "commons-db/migrate/schema"

const scriptLockTimeoutMillis = 30000

var batchSeparator = regexp.MustCompile(`(?mi)^\s*GO\s*$`)

func writeHeaderList(b *strings.Builder, title string, ids []string) {
	if len(ids) == 0 {
		fmt.Fprintf(b, "-- %s: none\n", title)
		return
	}
	fmt.Fprintf(b, "-- %s:\n", title)
	for _, id := range ids {
		fmt.Fprintf(b, "--   %s\n", id)
	}
}

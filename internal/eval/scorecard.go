package eval

import (
	"fmt"
	"io"
)

// PrintScorecard renders a Report as a human-readable scorecard. The format
// is shared by the everychat-ops eval CLI and the admin UI's eval modal.
func PrintScorecard(w io.Writer, rep *Report) {
	if rep == nil {
		_, _ = fmt.Fprintln(w, "(no report)")
		return
	}
	_, _ = fmt.Fprintf(w, "Eval: %s  (%d Fragen)\n", rep.Bot, rep.Total)
	for _, it := range rep.Items {
		mark := "PASS"
		if !it.Pass {
			mark = "FAIL"
		}
		line := fmt.Sprintf("  %s  %s", mark, it.ID)
		if !it.Pass && len(it.Reasons) > 0 {
			line += " — " + it.Reasons[0]
		}
		_, _ = fmt.Fprintln(w, line)
	}
	_, _ = fmt.Fprintln(w)
	verdict := "below"
	if rep.MeetsThreshold() {
		verdict = "above"
	}
	_, _ = fmt.Fprintf(w, "Score: %d/%d (%.2f) — %s threshold %.2f\n",
		rep.Passed, rep.Total, rep.Score, verdict, rep.Threshold)
}

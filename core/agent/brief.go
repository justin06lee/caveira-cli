package agent

import (
	"context"
	"fmt"
	"github.com/justin06lee/caveira/core/proc"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// The brief a compaction leaves the fresh context, built from the index of
// exchanges (exchanges.go) and git, with no model call. It opens with
// where things stand, gives the last few exchanges at length and the
// older ones as notes, and points each at its record file. It keeps to a
// budget, and what doesn't fit is named by its record files, not dropped.

// briefRecent is how many of the last exchanges the brief gives at length.
const briefRecent = 3

// briefBudgetChars is the brief's whole size, at most: some 10k tokens.
// A focused brief reads better than a long one.
const briefBudgetChars = 40_000

// How much of each recent exchange: the very last the most.
var (
	recentPromptChars = []int{4000, 2000, 2000}
	recentReplyChars  = []int{3000, 1500, 1500}
)

const listedPromptChars = 3000

const briefIntro = `You are a fresh context continuing a conversation that was compacted. The numbered exchanges below are that conversation, oldest first: "user wrote:" is their prompt, in their own words, "you:" is your reply. Treat them as your own memory; the user assumes you know all of it.
It opens with <state>: where things stand, read from git as this brief was made, facts rather than recollection; where a note disagrees with it, the state is right. Older replies are compressed to notes, except the last few (<recent>), which come at length.
Each exchange ends with "full:" and the path of a file holding its complete prompt, replies and tool activity. Whenever a note alone is not detailed enough to answer or act on, read that file with read_file instead of guessing.
`

// digest is the seam for condensing the oldest exchanges into one memory
// once there are many: it returns how many of xs it covers and the text
// standing for them. Nothing is condensed yet, so every exchange is listed
// or named by its file.
func digest(xs []Exchange) (covered int, text string) { return 0, "" }

// buildBrief writes the brief over the exchanges recorded so far, with the
// model's handoff note when there is one.
func buildBrief(dir, workDir string, xs []Exchange, handoff string) string {
	var head strings.Builder
	head.WriteString("<compacted-history>\n" + briefIntro + "\n")
	if state := stateLines(workDir, xs); len(state) > 0 {
		head.WriteString("<state>\n" + strings.Join(state, "\n") + "\n</state>\n\n")
	}
	if handoff != "" {
		head.WriteString("<handoff-note>\nThe note you wrote for this moment, from the whole conversation:\n" + cutMiddle(handoff, 12_000) + "\n</handoff-note>\n\n")
	}

	covered, condensed := digest(xs)
	if condensed != "" {
		head.WriteString("<condensed>\n" + condensed + "\n</condensed>\n\n")
	}
	xs = xs[covered:]

	recentFrom := max(0, len(xs)-briefRecent)
	var recent []string
	for i, x := range xs[recentFrom:] {
		rank := len(xs) - recentFrom - 1 - i
		recent = append(recent, atLength(dir, x, recentPromptChars[rank], recentReplyChars[rank]))
	}
	recentBlock := ""
	if len(recent) > 0 {
		recentBlock = "<recent>\n" + strings.Join(recent, "\n\n") + "\n</recent>\n"
	}
	tail := "</compacted-history>"

	// The listed exchanges with what room is left, newest first.
	// (less the line naming the exchanges left out, should there be some)
	room := briefBudgetChars - head.Len() - len(recentBlock) - len(tail) - 400
	var kept []string
	first := recentFrom
	for i := recentFrom - 1; i >= 0; i-- {
		line := listed(dir, xs[i])
		if len(line)+1 > room {
			break
		}
		kept = append([]string{line}, kept...)
		room -= len(line) + 1
		first = i
	}
	var b strings.Builder
	b.WriteString(head.String())
	if first > 0 {
		fmt.Fprintf(&b, "Exchanges %d–%d aren't listed here, for room; their full records are files %s to %s in %s.\n\n",
			xs[0].N, xs[first-1].N, filepath.Base(recordPath(dir, xs[0].N)), filepath.Base(recordPath(dir, xs[first-1].N)), filepath.Join(dir, "exchanges"))
	}
	if len(kept) > 0 {
		b.WriteString(strings.Join(kept, "\n") + "\n\n")
	}
	b.WriteString(recentBlock)
	b.WriteString(tail)
	return b.String()
}

func listed(dir string, x Exchange) string {
	return fmt.Sprintf("%d. user wrote:\n\"\"\"\n%s\n\"\"\"\n   you: %s\n   full: %s", x.N, cutMiddle(x.Prompt, listedPromptChars), orNone(x.ReplyNote), recordPath(dir, x.N))
}

func atLength(dir string, x Exchange, promptChars, replyChars int) string {
	return fmt.Sprintf("%d. user wrote:\n\"\"\"\n%s\n\"\"\"\n   you replied (its close, where it's long):\n\"\"\"\n%s\n\"\"\"\n   full: %s", x.N, cutMiddle(x.Prompt, promptChars), cutEnd(orNone(x.Reply), replyChars), recordPath(dir, x.N))
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(no text; see its record)"
	}
	return s
}

// stateLines is where things stand: the branch and what is uncommitted,
// as git tells it, the files the conversation changed, and what the last
// reply offered to do next.
func stateLines(workDir string, xs []Exchange) []string {
	var lines []string
	status := map[string]string{}
	if branch := git(workDir, "rev-parse", "--abbrev-ref", "HEAD"); branch != "" {
		line := "- Branch: " + branch
		if ab := git(workDir, "rev-list", "--left-right", "--count", "@{upstream}...HEAD"); ab != "" {
			if f := strings.Fields(ab); len(f) == 2 && (f[0] != "0" || f[1] != "0") {
				line += fmt.Sprintf(" (%s behind, %s ahead of its upstream)", f[0], f[1])
			}
		}
		lines = append(lines, line)
		var dirty []string
		for _, l := range strings.Split(git(workDir, "status", "--porcelain=v1"), "\n") {
			if len(l) > 3 {
				status[strings.TrimSpace(l[3:])] = strings.TrimSpace(l[:2])
				dirty = append(dirty, strings.TrimSpace(l))
			}
		}
		switch {
		case len(dirty) == 0:
			lines = append(lines, "- Uncommitted: nothing")
		case len(dirty) > 15:
			lines = append(lines, fmt.Sprintf("- Uncommitted: %d files — %s, …", len(dirty), strings.Join(dirty[:15], "; ")))
		default:
			lines = append(lines, fmt.Sprintf("- Uncommitted: %d files — %s", len(dirty), strings.Join(dirty, "; ")))
		}
	}

	byFile := map[string][]string{}
	var files []string
	for _, x := range xs {
		for _, f := range x.Files {
			if _, ok := byFile[f]; !ok {
				files = append(files, f)
			}
			byFile[f] = append(byFile[f], fmt.Sprint(x.N))
		}
	}
	if len(files) > 0 {
		var parts []string
		for _, f := range files[max(0, len(files)-30):] {
			part := fmt.Sprintf("%s [%s]", f, strings.Join(byFile[f], ", "))
			rel := f
			if filepath.IsAbs(f) {
				if r, err := filepath.Rel(workDir, f); err == nil {
					rel = r
				}
			}
			if st, ok := status[rel]; ok {
				part += " " + st
			}
			parts = append(parts, part)
		}
		more := ""
		if len(files) > 30 {
			more = fmt.Sprintf(" (and %d earlier)", len(files)-30)
		}
		lines = append(lines, "- Files this conversation changed, by the exchanges that changed them, with git's status now where uncommitted"+more+": "+strings.Join(parts, "; "))
	}
	if len(xs) > 0 {
		if _, next, ok := strings.Cut(xs[len(xs)-1].ReplyNote, " · next: "); ok {
			lines = append(lines, "- Your last reply offered to do next: "+next)
		}
	}
	return lines
}

// git runs git in dir, briefly, and gives its output, or "" on any failure.
func git(dir string, args ...string) string {
	if dir == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	proc.Hide(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(out), "\n")
}

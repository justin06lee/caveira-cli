package ui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// bubbleText wraps a message for a bubble against the right edge of width.
// Wide terminals keep the bubble to three quarters of the line, so it reads
// as a reply-side message rather than a banner.
func bubbleText(text string, width int) (lines []string, textW int) {
	maxW := min(max(width*3/4, 24), width)
	textCap := max(maxW-4, 8)
	lines = strings.Split(lipgloss.Wrap(text, textCap, ""), "\n")
	for _, l := range lines {
		textW = max(textW, lipgloss.Width(l))
	}
	return lines, textW
}

// bubble draws text as a chat bubble flush with the right edge of width: a
// block of background colour with a row of padding above and below the
// text and two cells either side. Every row is exactly as wide as the
// others and nothing but background colour makes the shape, so the edges
// line up on every terminal and font; a block character for a rounded edge
// would come from the font, and does not meet the rows beside it where the
// font or line spacing is not the one it was drawn for. Where there is no
// fill colour to draw with (16-colour and colourless terminals), it falls
// back to a rounded outline.
func bubble(text string, width int, fill, fg, outline color.Color) string {
	wrapped, textW := bubbleText(text, width)

	if fill == nil {
		box := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(outline).
			Foreground(fg).
			Padding(0, 1)
		b := box.Render(strings.Join(wrapped, "\n"))
		return lipgloss.PlaceHorizontal(width, lipgloss.Right, b)
	}

	w := textW + 4 // two cells of padding either side
	lead := strings.Repeat(" ", max(width-w, 0))
	body := lipgloss.NewStyle().Background(fill).Foreground(fg)
	blank := lead + body.Render(strings.Repeat(" ", w))

	out := make([]string, 0, len(wrapped)+2)
	out = append(out, blank)
	for _, l := range wrapped {
		pad := strings.Repeat(" ", textW-lipgloss.Width(l))
		out = append(out, lead+body.Render("  "+l+pad+"  "))
	}
	out = append(out, blank)
	return strings.Join(out, "\n")
}

// draftBubble is a message that has not been sent yet: the same size and
// place as a sent bubble, with the text in the same columns, but only a
// dashed outline where the fill would be.
func draftBubble(text string, width int, fg, outline color.Color) string {
	wrapped, textW := bubbleText(text, width)
	w := textW + 4
	lead := strings.Repeat(" ", max(width-w, 0))
	edge := lipgloss.NewStyle().Foreground(outline)
	body := lipgloss.NewStyle().Foreground(fg)

	out := make([]string, 0, len(wrapped)+2)
	out = append(out, lead+edge.Render("╭"+strings.Repeat("┄", w-2)+"╮"))
	for _, l := range wrapped {
		pad := strings.Repeat(" ", textW-lipgloss.Width(l))
		out = append(out, lead+edge.Render("┆")+" "+body.Render(l+pad)+" "+edge.Render("┆"))
	}
	out = append(out, lead+edge.Render("╰"+strings.Repeat("┄", w-2)+"╯"))
	return strings.Join(out, "\n")
}

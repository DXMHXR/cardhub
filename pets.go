package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Empty columns play a little looping pixel scene, Game Boy style: a pet with
// nothing to do (sleepy, lazy or hungry for cards) and a few props. Each
// column gets a different scene and runs on its own phase. The pet uses four
// shades of the column's colour, props are grey and accents yellow.
//
// Scenes are 22×16 pixels drawn with half blocks (▀ ▄ █): each character
// cell holds two stacked pixels, so pixels come out square.

const pxW, pxH = 22, 16

// pix is a scene's pixels; 0 is clear, otherwise a palette code (see sprites.go).
type pix [pxH][pxW]byte

// put draws sprite rows at x, y; '.' and ' ' are clear, off-canvas is clipped.
func (p *pix) put(x, y int, rows ...string) {
	for j, row := range rows {
		for i := 0; i < len(row); i++ {
			if ch := row[i]; ch != '.' && ch != ' ' {
				if yy, xx := y+j, x+i; yy >= 0 && yy < pxH && xx >= 0 && xx < pxW {
					p[yy][xx] = ch
				}
			}
		}
	}
}

// clear erases a w×h rectangle at x, y.
func (p *pix) clear(x, y, w, h int) {
	for yy := y; yy < y+h && yy < pxH; yy++ {
		for xx := x; xx < x+w && xx < pxW; xx++ {
			if yy >= 0 && xx >= 0 {
				p[yy][xx] = 0
			}
		}
	}
}

// mirror flips a sprite left to right.
func mirror(rows []string) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		b := []byte(r)
		for l, h := 0, len(b)-1; l < h; l, h = l+1, h-1 {
			b[l], b[h] = b[h], b[l]
		}
		out[i] = string(b)
	}
	return out
}

// pingPong counts 0, 1, … n-1 and back down, forever.
func pingPong(t, n int) int {
	if n < 2 {
		return 0
	}
	p := t % (2 * (n - 1))
	if p < n-1 {
		return p
	}
	return 2*(n-1) - p
}

// mood is how a pet behaves: idle (its sleepy, lazy or hungry loop), or awake
// because the cursor is on its column, startled for the first few steps.
type mood struct {
	awake bool
	since int // steps since the column got the cursor
}

const startleSteps = 5

func (md mood) startled() bool { return md.awake && md.since < startleSteps }

// jump is the vertical offset of a startled pet: up, up, down, landing.
func (md mood) jump() int {
	if !md.startled() {
		return 0
	}
	return []int{-3, -4, -3, -1, 0}[md.since]
}

// exclaim draws a "!" with its bottom at x, y (beside a pet, so a jump never
// pushes it off the top).
func exclaim(p *pix, x, y int) { p.put(x, y-4, "y", "y", "y", ".", "y") }

// scene draws step t of a loop (a step is ~300ms) in the given mood.
type scene func(t int, md mood) *pix

// twinkle sets star pixels that fade in and out.
func twinkle(p *pix, t int, spots [][2]int) {
	for i, s := range spots {
		switch (t + i*2) % 6 {
		case 0, 1:
			p.put(s[0], s[1], "y")
		case 2, 3, 4:
			p.put(s[0], s[1], "x")
		}
	}
}

// zzz floats a small "z" up and to the right from x, y.
func zzz(p *pix, t, x, y int) {
	if s := t % 8; s < 6 {
		p.put(x+s, y-s, "yyy", ".y.", "yyy")
	}
}

var spCrescent = []string{".xx", "x..", "x..", "x..", ".xx"}
var spBone = []string{"y....y", "yyyyyy", "y....y"}

const ground = "aaaaaaaaaaaaaaaaaaaaaa"

// lift is where a pet resting at y is drawn this step: jumping when startled,
// but never above the top of the scene.
func lift(y int, md mood) int { return max(0, y+md.jump()) }

var scenes = []scene{
	// sleepy cat curled up in its basket under the stars
	func(t int, md mood) *pix {
		p := &pix{}
		twinkle(p, t, [][2]int{{1, 1}, {7, 0}, {12, 2}, {20, 1}, {3, 3}})
		p.put(0, 10, sprBasket...)
		if !md.awake {
			p.put(2, 2-t/3%2, sprCat...) // breathing
			zzz(p, t, 18, 3)
			return p
		}
		y := lift(1, md) // head up, watching you
		p.put(2, y, sprCat...)
		p.put(4, y+5, "w1..w1")
		if md.startled() {
			exclaim(p, 20, 8)
		}
		return p
	},
	// hungry dog, chin on its paws by an empty bowl, dreaming of a bone
	func(t int, md mood) *pix {
		p := &pix{}
		p.put(0, 15, ground)
		p.put(11, 11, sprBowl...)
		if md.awake { // ears up, eyes wide: is it dinner?
			y := lift(1, md)
			p.put(0, y, sprDog...)
			p.put(4, y+5, "w")
			p.put(10, y+5, "w")
			if md.startled() {
				exclaim(p, 18, 8)
			} else if t%2 == 0 {
				p.put(15, y+6, "x") // happy wag
			}
			return p
		}
		p.put(0, 2, sprDog...)
		if t%9 == 4 { // blink
			p.put(4, 6, "1")
			p.put(10, 6, "1")
		}
		switch s := t % 16; {
		case s >= 4 && s < 12:
			p.put(17, 6, "c")
			if s >= 5 {
				p.put(15, 5, "c")
			}
			if s >= 6 {
				p.put(14, 0, ".cccccc.", "c......c", "c......c", "c......c", ".cccccc.")
				p.put(15, 1, spBone...)
			}
		case s == 12 || s == 13:
			p.put(13, 0, "x...y..x", "...x....", "y......y", "..x..x..")
		}
		return p
	},
	// sleepy owl on a branch under a crescent moon, a leaf drifting down
	func(t int, md mood) *pix {
		p := &pix{}
		p.put(1, 1, spCrescent...)
		p.put(0, 13, "bbbbbbbbbbbbbbbbbbbbbb", "....a.......a.......a.")
		if md.awake { // big round eyes, wide awake
			y := lift(1, md)
			p.put(5, y, sprOwl...)
			p.put(7, y+3, "www", "w1w", "www")
			p.put(13, y+3, "www", "w1w", "www")
			if md.startled() {
				exclaim(p, 20, 8)
			}
			return p
		}
		x := 5 + pingPong(t/3, 2) // swaying
		p.put(x, 1, sprOwl...)
		if s := t % 14; s == 9 || s == 10 { // peeks with one eye
			p.put(x+2, 5, "1w1")
		}
		if y := t % 13; y < 12 {
			p.put(18+pingPong(t, 3), y, "x")
		}
		zzz(p, t+3, 16, 3)
		return p
	},
	// hungry fish swimming round its bowl, gulping at the surface
	func(t int, md mood) *pix {
		p := &pix{}
		if md.awake {
			t *= 2 // swims twice as fast
		}
		p.put(2, 0, "...aaaaaaaaaaaa...", "..a............a..", ".a..............a.")
		for y := 3; y < 12; y++ {
			p.put(2, y, "a................a")
		}
		p.put(2, 12, ".a..............a.", "..aaaaaaaaaaaaaa..", ".bbbbbbbbbbbbbbbb.")
		p.put(4, 3, "bb..bb..bb..bb")
		const swim = 7                 // the bowl's inside is 16 pixels; the fish is 10
		x, y := 3+pingPong(t, swim), 5 // the inside runs from column 3 to 18
		fish := sprFish
		if t%(2*(swim-1)) >= swim-1 {
			fish = mirror(sprFish) // heading back: face left
		}
		if s := t % 15; s >= 11 { // up to the surface to gulp
			y = 4
		}
		p.put(x, max(3, y+md.jump()), fish...)
		if md.startled() {
			exclaim(p, 21, 8) // outside the bowl
		}
		for k := 0; k < 3; k++ {
			if q := (t + k*3) % 7; q < 3 && y-1-q >= 3 {
				p.put(x+8-k*3, y-1-q, []string{"w", "c"}[k%2])
			}
		}
		return p
	},
	// lazy turtle sunning on a rock, head slowly out… and back in
	func(t int, md mood) *pix {
		p := &pix{}
		sun := [][]string{{"..x..", ".xyx.", "xyyyx", ".xyx.", "..x.."}, {"x...x", ".xyx.", ".yyy.", ".xyx.", "x...x"}}[t/2%2]
		p.put(17, 0, sun...)
		p.put(0, 15, "bbbbbbbbbbbbbbbbbbbbbb")
		p.put(1, 11, sprRock...)
		p.put(1, 5, sprTurtle...)
		hide := t%20 >= 12 // head tucked in
		if md.awake {
			hide = md.startled() // startled: straight into its shell… then peeks out
		}
		if hide {
			p.clear(16, 6, 4, 3)
		}
		if md.startled() {
			exclaim(p, 9, 4)
		}
		return p
	},
	// two hungry chicks in their nest, beaks opening in turn
	func(t int, md mood) *pix {
		p := &pix{}
		p.put(0, 14, "bbbbbbbbbbbbbbbbbbbbbb")
		for k := 0; k < 2; k++ {
			x, y, spr := 4+k*8, 5, sprChick
			if md.awake { // both begging at once
				spr, y = sprChickOpen, lift(4, md)
			} else if (t/2+k)%2 == 0 {
				spr, y = sprChickOpen, 4 // stretching up, cheeping
				p.put(x+8, y-2+t%2, "x")
			}
			p.put(x, y, spr...)
		}
		if md.startled() {
			exclaim(p, 21, 8)
		}
		p.put(1, 10, sprNest...)
		return p
	},
	// sleepy robot on charge: a spark runs, the battery fills, it wakes…
	// then dozes off again
	func(t int, md mood) *pix {
		p := &pix{}
		s := t % 20
		if md.awake {
			s = 10 // powered on, battery full
		}
		p.put(0, 15, ground)
		p.put(20, 5, "b", "b", "b", "b", "b", "b", "b", "b", "b", "b")
		p.put(12, 12, "aaaaaaaa")
		p.put(19, 11, "c", "c")
		level, y, awake := 0, 4, false
		switch {
		case s < 10:
			level = s / 2
			p.put(12+(s%4)*2, 12, "y")
		case s < 14:
			level, awake = 5, true
			y -= s % 2 // hop
		default:
			level = max(0, 5-(s-14))
			awake = s < 16
			if s >= 17 {
				zzz(p, s-17, 11, 3)
			}
		}
		p.put(12, 0, "aaaaaaa", "a.....a", "aaaaaaa")
		p.put(13, 1, strings.Repeat("x", level))
		y = lift(y, md)
		p.put(2, y, sprRobot...)
		if md.startled() {
			exclaim(p, 17, 9)
		}
		if awake { // eyes open, antenna lit
			p.put(4, y+4, "w")
			p.put(11, y+4, "w")
			p.put(7, y, "yy")
		}
		return p
	},
	// lazy sloth hanging from a branch, swaying slowly, yawning
	func(t int, md mood) *pix {
		p := &pix{}
		p.put(0, 0, "cccccccccccccccccccccc", "..a......a.........a..")
		x := 4 + pingPong(t/2, 3)
		if md.awake { // eyes open, hanging still
			x = 5
			dy := max(0, -md.jump()/2)
			p.put(x, 1+dy, sprSloth...)
			p.put(x+4, 7+dy, "w1.1w")
			if md.startled() {
				exclaim(p, x+11, 6)
			}
			return p
		}
		p.put(x, 1, sprSloth...)
		if t%20 >= 16 { // yawn
			p.put(x+5, 8, "111", "1.1")
		}
		return p
	},
}

// palette returns the colour for each pixel code: the pet in shades of the
// column's colour, props grey, accents yellow; dimmer unless selected.
func palette(colorName string, sel bool) map[byte]string {
	base := ghColors[colorName]
	if base == "" {
		base = ghColors["GRAY"]
	}
	dim := func(hex string) string {
		if sel {
			return hex
		}
		return mixHex(hex, "#0d1117", 0.3)
	}
	return map[byte]string{
		'1': dim(mixHex(base, "#000000", 0.55)),
		'2': dim(base),
		'3': dim(mixHex(base, "#ffffff", 0.5)),
		'w': dim("#f0f6fc"),
		'a': "#30363d", 'b': "#484f58", 'c': "#6e7681",
		'x': dim("#e3b341"), 'y': dim("#f5de96"),
	}
}

// mixHex blends a toward b: amount 0 is a, 1 is b.
func mixHex(a, b string, amount float64) string {
	var ar, ag, ab, br, bg, bb int
	fmt.Sscanf(strings.TrimPrefix(a, "#"), "%02x%02x%02x", &ar, &ag, &ab)
	fmt.Sscanf(strings.TrimPrefix(b, "#"), "%02x%02x%02x", &br, &bg, &bb)
	m := func(x, y int) int { return x + int(float64(y-x)*amount+0.5) }
	return fmt.Sprintf("#%02x%02x%02x", m(ar, br), m(ag, bg), m(ab, bb))
}

// render turns pixels into text lines: each cell shows two stacked pixels with
// a half block, the top pixel as foreground and the bottom one as background.
func (p *pix) render(pal map[byte]string) []string {
	styles := map[[2]string]lipgloss.Style{}
	style := func(fgc, bgc string) lipgloss.Style {
		k := [2]string{fgc, bgc}
		if st, ok := styles[k]; ok {
			return st
		}
		st := lipgloss.NewStyle().Foreground(lipgloss.Color(fgc))
		if bgc != "" {
			st = st.Background(lipgloss.Color(bgc))
		}
		styles[k] = st
		return st
	}
	lines := make([]string, pxH/2)
	for row := 0; row < pxH/2; row++ {
		var b strings.Builder
		for x := 0; x < pxW; x++ {
			top, bot := pal[p[row*2][x]], pal[p[row*2+1][x]]
			switch {
			case top == "" && bot == "":
				b.WriteByte(' ')
			case bot == "":
				b.WriteString(style(top, "").Render("▀"))
			case top == "":
				b.WriteString(style(bot, "").Render("▄"))
			case top == bot:
				b.WriteString(style(top, "").Render("█"))
			default:
				b.WriteString(style(top, bot).Render("▀"))
			}
		}
		lines[row] = b.String()
	}
	return lines
}

// petFits reports whether a whole scene (and its "no match" line while
// searching) fits in a space w wide and h tall; a clipped pet isn't shown.
func (m model) petFits(w, h int) bool {
	need := pxH / 2 // two pixels per character cell
	if strings.TrimSpace(m.filter.Value()) != "" {
		need++
	}
	return w >= pxW && h >= need
}

// emptyState plays column ci's scene (with a small "no match" under it while a
// search finds nothing there), centred in a space inner wide and h tall.
func (m model) emptyState(ci int, c column, inner, h int, sel bool) string {
	t := (m.frame + ci*11) / 2 // ~300ms a step, each column on its own phase
	var md mood
	if sel { // the cursor woke it up
		md = mood{awake: true, since: (m.frame - m.focusFrame) / 2}
	}
	lines := scenes[ci%len(scenes)](t, md).render(palette(c.color, sel))
	for i, l := range lines {
		lines[i] = lipgloss.PlaceHorizontal(inner, lipgloss.Center, l)
	}
	if strings.TrimSpace(m.filter.Value()) != "" {
		lines = append(lines, lipgloss.PlaceHorizontal(inner, lipgloss.Center, dim.Render("no match")))
	}
	return lipgloss.Place(inner, h, lipgloss.Center, lipgloss.Center, strings.Join(lines, "\n"))
}

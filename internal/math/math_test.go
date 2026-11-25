package math_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yuin/goldmark/v2/extension"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/renderer/html"

	"github.com/anantshri/markbrowse/internal/math"
)

func render(t *testing.T, src string) string {
	t.Helper()
	p := parser.New(
		parser.WithExtensions(extension.GFMParser, math.NewParser()),
	)
	r := html.New(
		html.WithExtensions(extension.GFMHTMLRenderer, math.NewHTMLRenderer()),
	)
	var buf bytes.Buffer
	doc := p.Parse([]byte(src))
	if err := r.Render(&buf, []byte(src), doc); err != nil {
		t.Fatalf("render error: %v", err)
	}
	return buf.String()
}

func TestInlineMath(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "underscores preserved without emphasis",
			src:  `weight $N_{c,r}$ and $a_1 + b_2$`,
			want: `<p>weight <span class="math-inline">N_{c,r}</span>` +
				` and <span class="math-inline">a_1 + b_2</span></p>`,
		},
		{
			name: "backslashes preserved",
			src:  `($4\pi A / P^2$) and $\{0..4\}$`,
			want: `<p>(<span class="math-inline">4\pi A / P^2</span>)` +
				` and <span class="math-inline">\{0..4\}</span></p>`,
		},
		{
			name: "wrapped across lines in paragraph",
			src:  "solves $\\min_{\\hat{q}_1} \\sum_i (\n  \\hat{q}_i)^2$ ok",
			want: `<span class="math-inline">\min_{\hat{q}_1} \sum_i (` +
				"\n" + `\hat{q}_i)^2</span> ok`,
		},
		{
			name: "inside bold markers",
			src:  `**$L_2$-Regularized** and **$p_{\text{civ}} = 0.785$**`,
			want: `<strong><span class="math-inline">L_2</span>` +
				`-Regularized</strong> and <strong>` +
				`<span class="math-inline">p_{\text{civ}} = 0.785</span>` +
				`</strong>`,
		},
		{
			name: "inside GFM table cell",
			src:  "| a | b |\n|---|---|\n| x | $p_{(1)} \\ge 0.85$ |\n",
			want: `<td><span class="math-inline">p_{(1)} \ge 0.85</span></td>`,
		},
		{
			name: "escaped dollar inside math does not close early",
			src:  `$\text{cost} = \$5$`,
			want: `<p><span class="math-inline">\text{cost} = \$5</span></p>`,
		},
		{
			name: "double backslash before closing dollar closes span",
			src:  `$a \\$`,
			want: `<p><span class="math-inline">a \\</span></p>`,
		},
		{
			name: "inline double-dollar in paragraph",
			src:  `before $$x_1 + x_2$$ after`,
			want: `<p>before <span class="math-display">x_1 + x_2</span>` +
				` after</p>`,
		},
		{
			name: "two double-dollar spans on one line",
			src:  `$$a_1$$ and $$b_2$$`,
			want: `<p><span class="math-display">a_1</span> and ` +
				`<span class="math-display">b_2</span></p>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, tt.src)
			if !strings.Contains(got, tt.want) {
				t.Errorf("render(%q) =\n%s\nwant substring:\n%s",
					tt.src, got, tt.want)
			}
		})
	}
}

func TestBlockMath(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "multiline aligned with indented lines",
			src: "$$\n\\begin{aligned}\n" +
				"    a &= b \\\\\n" +
				"    c &= d_1 + d_2\n" +
				"\\end{aligned}\n$$\n",
			want: "<div class=\"math-display\">\\begin{aligned}\n" +
				"    a &amp;= b \\\\\n" +
				"    c &amp;= d_1 + d_2\n" +
				"\\end{aligned}\n</div>",
		},
		{
			name: "single line block",
			src:  "$$E = m c^2$$\n\nNext.\n",
			want: "<div class=\"math-display\">E = m c^2</div>\n<p>Next.</p>",
		},
		{
			name: "indented block inside bullet list",
			src: "- item:\n\n  $$\n  w_i = \\frac{\\pi_c}{K_c}\n  $$\n\n  " +
				"after.\n",
			want: "<div class=\"math-display\">w_i = \\frac{\\pi_c}{K_c}\n" +
				"</div>\n<p>after.</p>",
		},
		{
			name: "closing fence on last expression line",
			src:  "$$a_1 +\nb_2$$\n",
			want: "<div class=\"math-display\">a_1 +\nb_2</div>",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, tt.src)
			if !strings.Contains(got, tt.want) {
				t.Errorf("render(%q) =\n%s\nwant substring:\n%s",
					tt.src, got, tt.want)
			}
		})
	}
}

func TestNonMathPreserved(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{"single currency", "costs $5 today"},
		{"two currencies", "costs $5 and $10 today"},
		{"currency range", "between $5-$10"},
		{"spaced dollars", "not $ x + y $ math"},
		{"escaped dollars", `not \$x_1\$ math`},
		{"triple dollars", "literal $$$ here"},
		{"unclosed double dollar", "literal $$ here"},
		{"code span", "use `$x_1$` verbatim"},
		{"fenced code", "```\n$$\nx_1\n$$\n```\n"},
		{"indented code block", "    $$x_1$$\n"},
		{"silverbullet anchor", "## Goals $proj/doc/goals\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(t, tt.src)
			if strings.Contains(got, `class="math-`) {
				t.Errorf("render(%q) unexpectedly matched math:\n%s",
					tt.src, got)
			}
		})
	}
}

func TestCurrencyBeforeRealMath(t *testing.T) {
	src := "Price is $5 and $10, where $x_1 = 2$."
	got := render(t, src)
	want := `<p>Price is $5 and $10, where ` +
		`<span class="math-inline">x_1 = 2</span>.</p>`
	if strings.TrimSpace(got) != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestMathHTMLEscaping(t *testing.T) {
	src := `$a < b & c > d$ and $<script>alert(1)</script>$` +
		"\n\n$$\n<img src=x onerror=alert(1)>\n$$\n"
	got := render(t, src)
	for _, bad := range []string{"<script>", "<img"} {
		if strings.Contains(got, bad) {
			t.Errorf("unescaped %q in:\n%s", bad, got)
		}
	}
	if !strings.Contains(got, `&lt;script&gt;`) ||
		!strings.Contains(got, `a &lt; b &amp; c &gt; d`) {
		t.Errorf("expected escaped HTML entities in:\n%s", got)
	}
}

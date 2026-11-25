// Package math parses and renders TeX math expressions ($...$ inline and
// $$...$$ display) for client-side KaTeX rendering.
package math

import (
	"bytes"
	"fmt"
	"io"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/renderer"
	"github.com/yuin/goldmark/v2/renderer/html"
	"github.com/yuin/goldmark/v2/text"
	"github.com/yuin/goldmark/v2/util"
)

// KindBlock is the AST node kind for a $$...$$ display math block.
var KindBlock = ast.NewNodeKind("MathBlock")

// KindInline is the AST node kind for an inline $...$ or $$...$$ expression.
var KindInline = ast.NewNodeKind("MathInline")

// Block holds verbatim display math lines for client-side rendering.
type Block struct {
	ast.BaseBlock

	Value text.Lines
}

// Kind implements ast.Node.
func (n *Block) Kind() ast.NodeKind { return KindBlock }

// Dump implements ast.Node.
func (n *Block) Dump(_ []byte) *ast.NodeDump {
	return ast.NewNodeDump(n, nil)
}

// NewBlock creates a display math block node.
func NewBlock() *Block {
	n := &Block{}
	n.Init(n)
	return n
}

// Inline holds a verbatim inline math expression for client-side rendering.
type Inline struct {
	ast.BaseInline

	Value   text.Value
	Display bool
}

// Kind implements ast.Node.
func (n *Inline) Kind() ast.NodeKind { return KindInline }

// Dump implements ast.Node.
func (n *Inline) Dump(_ []byte) *ast.NodeDump {
	return ast.NewNodeDump(n, map[string]any{"Display": n.Display})
}

// NewInline creates an inline math node.
func NewInline(value text.Value, display bool) *Inline {
	n := &Inline{Value: value, Display: display}
	n.Init(n)
	return n
}

type blockData struct {
	indent int
	node   *Block
	closed bool
}

var blockDataKey = parser.NewContextKey()

var doubleDollar = []byte("$$")

// blockParser parses $$...$$ display math blocks.
type blockParser struct{}

var _ parser.BlockParser = (*blockParser)(nil)

func (b *blockParser) Trigger() []byte { return []byte{'$'} }

func (b *blockParser) Open(
	_ ast.Node, reader text.Reader, pc parser.Context,
) (ast.Node, parser.State) {
	line, seg := reader.PeekLine()
	pos := pc.BlockOffset()
	if pos < 0 || pos+1 >= len(line) ||
		line[pos] != '$' || line[pos+1] != '$' {
		return nil, parser.NoChildren
	}

	rest := line[pos+2:]
	trimmedLen := len(rest) - util.TrimRightSpaceLength(rest)
	trimmed := rest[:trimmedLen]

	node := NewBlock()
	closed := false
	contentStart := seg.Start - seg.Padding + pos + 2

	switch {
	case len(trimmed) == 0:
		// Multi-line block: content starts on the next line.
	case bytes.HasSuffix(trimmed, doubleDollar):
		inner := trimmed[:len(trimmed)-2]
		if bytes.Contains(inner, doubleDollar) {
			// Two $$...$$ spans on one line: let inline parser handle it.
			return nil, parser.NoChildren
		}
		if len(inner) > 0 {
			s := text.NewSegment(contentStart, contentStart+len(inner))
			node.Value.AppendSegment(s)
		}
		closed = true
	case bytes.Contains(trimmed, doubleDollar):
		// Inline $$...$$ followed by trailing paragraph text.
		return nil, parser.NoChildren
	default:
		// Opening $$ with first expression line immediately following.
		s := text.NewSegment(contentStart, seg.Stop)
		s.ForceNewline = true
		node.Value.AppendSegment(s)
	}

	pc.Set(blockDataKey, &blockData{
		indent: pc.BlockIndent(),
		node:   node,
		closed: closed,
	})
	return node, parser.NoChildren
}

func (b *blockParser) Continue(
	node ast.Node, reader text.Reader, pc parser.Context,
) parser.State {
	data, ok := pc.Get(blockDataKey).(*blockData)
	if !ok || data.node != node || data.closed {
		return parser.Close
	}

	line, seg := reader.PeekLine()
	trimmedLen := len(line) - util.TrimRightSpaceLength(line)
	w, pos := util.IndentWidth(line, reader.LineOffset())

	// Standalone closing $$ fence (up to 3 spaces indent).
	if w < 4 && pos < trimmedLen &&
		bytes.Equal(line[pos:trimmedLen], doubleDollar) {
		reader.AdvanceToEOL()
		return parser.Close
	}

	block := node.(*Block)

	// Closing $$ at the end of a content line (e.g. \end{aligned}$$).
	if trimmedLen >= 2 &&
		line[trimmedLen-2] == '$' && line[trimmedLen-1] == '$' {
		prefixEnd := trimmedLen - 2
		start, padding := stripBlockIndent(
			line[:prefixEnd], reader.LineOffset(), seg.Padding, data.indent,
		)
		startByte := seg.Start + start
		stopByte := seg.Start - seg.Padding + prefixEnd
		if stopByte > startByte {
			s := text.NewSegmentPadding(startByte, stopByte, padding)
			block.Value.AppendSegment(s)
		}
		reader.AdvanceToEOL()
		return parser.Close
	}

	start, padding := stripBlockIndent(
		line, reader.LineOffset(), seg.Padding, data.indent,
	)
	s := text.NewSegmentPadding(seg.Start+start, seg.Stop, padding)
	s.ForceNewline = true
	block.Value.AppendSegment(s)
	reader.AdvanceToEOL()
	return parser.Continue | parser.NoChildren
}

func stripBlockIndent(
	line []byte, lineOffset, segPadding, indent int,
) (int, int) {
	pos, padding := util.IndentPositionPadding(
		line, lineOffset, segPadding, indent,
	)
	if pos < 0 {
		pos = max(0, util.FirstNonSpacePosition(line)) - segPadding
		padding = 0
	}
	return pos, padding
}

func (b *blockParser) Close(node ast.Node, _ text.Reader, pc parser.Context) {
	if data, ok := pc.Get(blockDataKey).(*blockData); ok && data.node == node {
		pc.Set(blockDataKey, nil)
	}
}

func (b *blockParser) CanInterruptParagraph() bool { return true }
func (b *blockParser) CanAcceptIndentedLine() bool { return false }

// inlineParser parses $...$ and inline $$...$$ spans, including spans wrapped
// across lines in a paragraph.
type inlineParser struct{}

var _ parser.InlineParser = (*inlineParser)(nil)

func (p *inlineParser) Trigger() []byte { return []byte{'$'} }

func (p *inlineParser) Parse(
	_ ast.Node, block text.Reader, _ parser.Context,
) ast.Node {
	line, startSeg := block.PeekLine()
	opener := 0
	for opener < len(line) && line[opener] == '$' {
		opener++
	}
	if opener == 0 {
		return nil
	}
	if opener > 2 {
		// Consume the run as literal text so trailing $ aren't re-parsed.
		block.Advance(opener)
		seg := startSeg.WithStop(startSeg.Start + opener)
		return ast.NewText(
			text.NewSingleLineValueFromSegment(seg, block.Decoder()),
		)
	}

	// Single-dollar inline math cannot open immediately before whitespace.
	if opener == 1 && (len(line) <= 1 || isMathSpace(line[1])) {
		return nil
	}

	block.Advance(opener)
	savedLine, savedPos := block.Position()

	var builder text.ValueBuilder
	firstLine := true
	backslashes := 0

	for {
		cur, seg := block.PeekLine()
		if cur == nil {
			block.SetPosition(savedLine, savedPos)
			if opener == 2 {
				// Leave unclosed $$ as literal text so the second $ is not
				// retried as a single-dollar opener.
				openSeg := startSeg.WithStop(startSeg.Start + opener)
				v := text.NewSingleLineValueFromSegment(
					openSeg, block.Decoder(),
				)
				return ast.NewText(v)
			}
			return nil
		}

		for i := 0; i < len(cur); i++ {
			c := cur[i]
			if c == '\\' {
				backslashes++
				continue
			}
			if c == '$' && backslashes%2 == 0 {
				oldi := i
				for i < len(cur) && cur[i] == '$' {
					i++
				}
				closure := i - oldi

				if opener == 1 {
					// Unescaped $ is invalid inside TeX math, so the first one
					// must be a valid closer; otherwise abort so currency like
					// "$5 and $10, where $x$" does not swallow across spans.
					if closure != 1 ||
						oldi == 0 || isMathSpace(cur[oldi-1]) ||
						(i < len(cur) && isASCIIDigit(cur[i])) {
						return nil
					}
					if n := buildInlineNode(
						&builder, seg, oldi, firstLine, false,
					); n != nil {
						block.Advance(i)
						return n
					}
					return nil
				}

				if closure == 2 {
					if n := buildInlineNode(
						&builder, seg, oldi, firstLine, true,
					); n != nil {
						block.Advance(i)
						return n
					}
					block.SetPosition(savedLine, savedPos)
					openSeg := startSeg.WithStop(startSeg.Start + opener)
					return ast.NewText(
						text.NewSingleLineValueFromSegment(
							openSeg, block.Decoder(),
						),
					)
				}
				// Re-check cur[i] on the next iteration.
				i--
			}
			backslashes = 0
		}

		builder.AddSegment(seg)
		block.AdvanceLine()
		firstLine = false
		backslashes = 0
	}
}

func buildInlineNode(
	builder *text.ValueBuilder,
	seg text.Segment,
	endOffset int,
	firstLine bool,
	display bool,
) ast.Node {
	if firstLine {
		idx := text.NewIndex(seg.Start, seg.Start+endOffset)
		if idx.IsEmpty() {
			return nil
		}
		v := text.NewSingleLineValueFromIndex(idx, text.IdentityDecoder)
		return NewInline(v, display)
	}
	if endOffset > 0 {
		builder.AddIndex(text.NewIndex(seg.Start, seg.Start+endOffset))
	}
	v := builder.BuildMultiLine()
	if v.IsEmpty() {
		return nil
	}
	return NewInline(v, display)
}

func isMathSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

func isASCIIDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

type blockRenderer struct{}

func (r *blockRenderer) Render(
	w io.Writer, source []byte, node ast.Node, entering bool,
	rc renderer.Context,
) (ast.WalkStatus, error) {
	n, ok := node.(*Block)
	if !ok {
		return ast.WalkContinue, nil
	}
	bw, ok := w.(util.BufWriter)
	if !ok {
		return ast.WalkStop, fmt.Errorf(
			"math: renderer needs a util.BufWriter, got %T", w,
		)
	}
	if !entering {
		_, _ = bw.WriteString("</div>\n")
		return ast.WalkContinue, nil
	}
	_, _ = bw.WriteString(`<div class="math-display">`)
	_, _ = n.Value.WriteTo(html.ContextTextWriter(rc), source)
	return ast.WalkContinue, nil
}

type inlineRenderer struct{}

func (r *inlineRenderer) Render(
	w io.Writer, source []byte, node ast.Node, entering bool,
	rc renderer.Context,
) (ast.WalkStatus, error) {
	n, ok := node.(*Inline)
	if !ok {
		return ast.WalkContinue, nil
	}
	bw, ok := w.(util.BufWriter)
	if !ok {
		return ast.WalkStop, fmt.Errorf(
			"math: renderer needs a util.BufWriter, got %T", w,
		)
	}
	if !entering {
		_, _ = bw.WriteString("</span>")
		return ast.WalkContinue, nil
	}
	if n.Display {
		_, _ = bw.WriteString(`<span class="math-display">`)
	} else {
		_, _ = bw.WriteString(`<span class="math-inline">`)
	}
	_, _ = n.Value.WriteTo(html.ContextTextWriter(rc), source)
	return ast.WalkContinue, nil
}

type parserExtension struct{}

// NewParser returns the math parser extension.
func NewParser() parser.Extension { return &parserExtension{} }

func (e *parserExtension) ParserOptions(_ *parser.Config) []parser.Option {
	return []parser.Option{
		parser.WithBlockParsers(
			util.Prioritized[parser.BlockParser](&blockParser{}, 701),
		),
		parser.WithInlineParsers(
			util.Prioritized[parser.InlineParser](&inlineParser{}, 150),
		),
	}
}

type htmlRendererExtension struct{}

// NewHTMLRenderer returns the math HTML renderer extension.
func NewHTMLRenderer() html.Extension { return &htmlRendererExtension{} }

func (e *htmlRendererExtension) RendererOptions(_ *html.Config) []html.Option {
	return []html.Option{
		html.WithNodeRenderers(map[ast.NodeKind]html.NodeRenderer{
			KindBlock:  &blockRenderer{},
			KindInline: &inlineRenderer{},
		}),
	}
}

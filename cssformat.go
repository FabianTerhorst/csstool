package csstool

import (
	"bufio"
	"bytes"
	"io"
	"log"

	"github.com/tdewolff/parse/css"
)

type stack []io.Writer

func (s stack) Push(v io.Writer) stack {
	return append(s, v)
}

func (s stack) Pop() (stack, io.Writer) {
	l := len(s)
	return s[:l-1], s[l-1]
}

func inArray(ary []string, val string) bool {
	for _, a := range ary {
		if val == a {
			return true
		}
	}
	return false
}

// CSSFormat contains formatting perferances for CSS
type CSSFormat struct {
	Indent          int
	IndentTab       bool
	AlwaysSemicolon bool
	RemoveAtRule    []string // ignore things like "@media XXX"
	Debug           bool
	Matcher         matcher
}

// NewCSSFormat creates an initialized CSSFormat object
func NewCSSFormat(indent int, useTabs bool, m matcher) *CSSFormat {
	if useTabs {
		indent = 1
	}
	if m == nil {
		m = &EmptyMatcher{}
	}
	return &CSSFormat{
		Indent:    indent,
		IndentTab: useTabs,
		Matcher:   m,
	}
}

// Format reformats CSS using a reader to output writer
func (c *CSSFormat) Format(r io.Reader, wraw io.Writer) error {
	var err error
	var w io.Writer
	writers := make(stack, 0)
	wbuf := bufio.NewWriter(wraw)

	// w is the main writer that is used.
	//  adjusted to whatever the correct destination is
	w = wbuf

	// various states
	qualified := 0
	ruleCount := 0
	indent := 0
	skipRuleset := false
	rulesetCount := 0

	p := css.NewParser(r, false)
	for {
		gt, _, data := p.Next()
		switch gt {
		case css.ErrorGrammar:
			wbuf.Flush()
			if err == io.EOF {
				err = nil
			}
			return err

		// a comma-separated list of tags
		// but not the last one .. so h1,h2,h3
		// h1,h2 are here, but h3 is a beginRuleSetGrammar
		case css.QualifiedRuleGrammar:
			tokens := p.Values()
			if c.Matcher.Remove(primarySelector(tokens)) {
				if c.Debug {
					log.Printf("cutting qualified rule %q due to %q", completeSelector(tokens), primarySelector(tokens))
				}
				continue
			}
			if qualified == 0 {
				c.addIndent(w, indent)
			} else {
				c.writeComma(w)
			}
			qualified++
			for _, t := range tokens {
				w.Write(t.Data)
			}
		case css.BeginRulesetGrammar:
			ruleCount = 0
			tokens := p.Values()
			if qualified == 0 {
				if c.Matcher.Remove(primarySelector(tokens)) {
					if c.Debug {
						log.Printf("cutting ruleset1 %q due to %q", completeSelector(tokens), primarySelector(tokens))
					}
					indent++
					skipRuleset = true
					continue
				}
				c.addIndent(w, indent)
				c.writeTokens(w, tokens)
				c.writeLeftBrace(w)
				indent++
				continue
			}

			qualified = 0
			indent++
			if c.Matcher.Remove(primarySelector(tokens)) {
				if c.Debug {
					log.Printf("cutting qualified rule %q due to %q", completeSelector(tokens), primarySelector(tokens))
				}
				c.writeLeftBrace(w)
				continue
			}
			c.writeComma(w)
			c.writeTokens(w, tokens)
			c.writeLeftBrace(w)
		case css.EndRulesetGrammar:
			indent--
			if skipRuleset {
				skipRuleset = false
				continue
			}
			rulesetCount++

			// add semicolon, even if the last rule
			// i.e.  color: #000;}
			if c.AlwaysSemicolon {
				w.Write([]byte{';'})
			}
			c.writeRightBrace(w, indent)
		case css.BeginAtRuleGrammar:

			// first compute the entire AtRule
			atRule := []byte{}
			atRule = append(atRule, data...)
			tokens := p.Values()

			// for '@media page', '@media' and 'page' has a required
			// whitespace token and so must alway be printed.  No
			// need to write extra whitespace
			//
			// for '@media (page-width:...)' the whitespace between them
			// is optional.  Print it if desired.
			if len(tokens) > 0 && tokens[0].TokenType != css.WhitespaceToken && c.Indent != 0 {
				atRule = append(atRule, ' ')
			}
			for _, tok := range tokens {
				atRule = append(atRule, tok.Data...)
			}
			// skip the "@media print" query
			if inArray(c.RemoveAtRule, string(atRule)) {
				// just skip over everything
				nestdepth := 1
				for nestdepth != 0 {
					gt, _, _ = p.Next()

					if gt == css.ErrorGrammar {
						wbuf.Flush()
						return nil
					}
					if gt == css.EndAtRuleGrammar {
						nestdepth--
					}
					if gt == css.BeginAtRuleGrammar {
						nestdepth++
					}
				}
				continue
			}

			ruleCount = 0
			rulesetCount = 0

			// first render the @rule
			// into it's own buffer

			// save existing context
			writers = writers.Push(w)

			w = &bytes.Buffer{}
			c.addIndent(w, indent)
			w.Write(atRule)
			c.writeLeftBrace(w)

			// set up new buffer for content
			writers = writers.Push(w)
			w = &bytes.Buffer{}
			indent++
		case css.EndAtRuleGrammar:
			// have we written anything?
			contents := w.(*bytes.Buffer).Bytes()
			writers, w = writers.Pop()
			header := w.(*bytes.Buffer).Bytes()
			writers, w = writers.Pop()
			indent--
			if len(contents) == 0 {
				// no
				continue
			}
			w.Write(header)
			w.Write(contents)
			c.writeRightBrace(w, indent)
		case css.CommentGrammar:
			w.Write(data)
			c.addNewline(w)
		case css.CustomPropertyGrammar:
			if skipRuleset {
				continue
			}
			c.addIndent(w, indent)
			w.Write(data)
			// do not add space
			w.Write([]byte{':'})
			c.writeTokens(w, p.Values())
			c.writeSemicolon(w)
		case css.DeclarationGrammar:
			if skipRuleset {
				continue
			}
			if ruleCount != 0 {
				c.writeSemicolon(w)
			}
			ruleCount++
			c.addIndent(w, indent)
			w.Write(data)
			w.Write([]byte{':'})
			c.addSpace(w)
			tokens := p.Values()
			for _, tok := range tokens {
				// add space before !important
				if len(tok.Data) == 1 && tok.Data[0] == '!' {
					c.addSpace(w)
				}
				w.Write(tok.Data)
			}
		case css.TokenGrammar:
			w.Write(data)
		case css.AtRuleGrammar:
			c.addIndent(w, indent)
			w.Write(data)
			c.writeTokens(w, p.Values())
			c.writeSemicolon(w)
		default:
			panic("Unknown grammar: " + gt.String() + " " + string(data))
		}
	}
}

var (
	spaces = []byte("                  ")
	tabs   = []byte("\t\t\t\t")
)

func (c *CSSFormat) addIndent(w io.Writer, depth int) {
	if c.Indent == 0 || depth == 0 {
		return
	}
	if c.IndentTab {
		w.Write(tabs[:depth])
		return
	}

	w.Write(spaces[:c.Indent*depth])
}
func (c *CSSFormat) addSpace(w io.Writer) {
	if c.Indent == 0 {
		return
	}
	w.Write([]byte{' '})
}

func (c *CSSFormat) addNewline(w io.Writer) {
	if c.Indent == 0 {
		return
	}
	w.Write([]byte{'\n'})
}

func (c *CSSFormat) writeComma(w io.Writer) {
	if c.Indent == 0 {
		w.Write([]byte{','})
		return
	}
	w.Write([]byte{',', ' '})
}

func (c *CSSFormat) writeLeftBrace(w io.Writer) {
	if c.Indent == 0 {
		w.Write([]byte{'{'})
		return
	}
	w.Write([]byte{' ', '{', '\n'})
}
func (c *CSSFormat) writeRightBrace(w io.Writer, depth int) {
	if c.Indent == 0 {
		w.Write([]byte{'}'})
		return
	}
	c.addNewline(w)
	c.addIndent(w, depth)
	w.Write([]byte{'}'})
	c.addNewline(w)
}

func (c *CSSFormat) writeSemicolon(w io.Writer) {
	if c.Indent == 0 {
		w.Write([]byte{';'})
		return
	}
	w.Write([]byte{';', '\n'})
}

func (c *CSSFormat) writeTokens(w io.Writer, tokens []css.Token) {
	for _, tok := range tokens {
		w.Write(tok.Data)
	}
}

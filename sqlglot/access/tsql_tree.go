package access

import "strings"

// tsqlBody reads one body: its statements, nested in the IF, WHILE,
// BEGIN … END and TRY … CATCH that hold them.
type tsqlBody struct {
	f         *fileReader
	text      string
	firstLine int
	entries   []tsqlEntry
	next      int
}

// tsqlEntry is one statement or control-flow marker of a body, in order.
type tsqlEntry struct {
	kind      segmentKind
	statement tsqlStatement
	line      int
}

// read reads the body from offset start on.
func (b *tsqlBody) read(start int) []Node {
	for _, seg := range splitSegments(b.text, start) {
		if seg.kind != segmentStatements {
			b.entries = append(b.entries, tsqlEntry{kind: seg.kind, line: b.firstLine + lineAt(b.text, seg.start) - 1})
			continue
		}
		for _, statement := range b.runStatements(seg.start, seg.end) {
			b.entries = append(b.entries, tsqlEntry{kind: segmentStatements, statement: statement, line: statement.line})
		}
	}
	var body []Node
	for b.next < len(b.entries) {
		if b.peek(segmentEnd) {
			b.diagnoseMarker("END closes no BEGIN")
			continue
		}
		body = append(body, b.one()...)
	}
	return body
}

func (b *tsqlBody) peek(kind segmentKind) bool {
	return b.next < len(b.entries) && b.entries[b.next].kind == kind
}

func (b *tsqlBody) diagnoseMarker(message string) {
	entry := b.entries[b.next]
	b.next++
	b.f.diagnose(entry.line, strings.TrimSpace(lineAround(b.f.src, entry.line)), message)
}

// one reads the next statement with everything it governs: a BEGIN its
// block, a TRY its CATCH, an IF or WHILE the statement after it. At an END or
// the body's end it reads nothing.
func (b *tsqlBody) one() []Node {
	if b.next == len(b.entries) || b.peek(segmentEnd) {
		return nil
	}
	entry := b.entries[b.next]
	b.next++
	switch entry.kind {
	case segmentBegin, segmentBeginCatch:
		return b.block()
	case segmentBeginTry:
		try := Node{Kind: TryNode, Line: entry.line, Then: b.block()}
		if b.peek(segmentBeginCatch) {
			b.next++
			try.Catch, try.HasCatch = b.block(), true
		}
		return []Node{try}
	case segmentElse:
		b.next--
		b.diagnoseMarker("ELSE follows no IF")
		return nil
	}
	read := entry.statement
	if read.err != nil {
		b.f.diagnose(read.line, read.text, read.err.Error())
		return nil
	}
	if read.cond != nil {
		return []Node{b.controlled(read)}
	}
	if len(read.read.accesses) == 0 && len(read.read.dynamic) == 0 {
		return nil
	}
	return []Node{{Kind: StatementNode, Line: read.line, Text: read.text, Accesses: read.read.accesses, Dynamic: read.read.dynamic}}
}

// block reads the statements up to the END that closes the BEGIN just read.
func (b *tsqlBody) block() []Node {
	var nodes []Node
	for b.next < len(b.entries) && !b.peek(segmentEnd) {
		nodes = append(nodes, b.one()...)
	}
	if b.next < len(b.entries) {
		b.next++
	}
	return nodes
}

// controlled is the IF or WHILE read opens, over the statement it governs.
func (b *tsqlBody) controlled(read tsqlStatement) Node {
	node := Node{Kind: IfNode, Line: read.line, Text: read.text, Accesses: read.read.accesses, Condition: read.cond.text}
	if read.cond.loop {
		node.Kind = WhileNode
	}
	if !read.cond.jumps {
		node.Then = b.one()
	}
	if !read.cond.loop && b.peek(segmentElse) {
		b.next++
		node.Else, node.HasElse = b.one(), true
	}
	return node
}

// lineAround is the text of the 1-based line of text.
func lineAround(text string, line int) string {
	lines := strings.SplitN(text, "\n", line+1)
	if line > len(lines) {
		return ""
	}
	return lines[line-1]
}

package neopidialect

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
)

// StreamEvent is one AssistantMessageEvent as a provider stream emits it:
// "start", "text_start"/"text_delta"/"text_end", the thinking and toolcall
// counterparts, then "done" or "error". Block is the tool call of the
// toolcall events.
type StreamEvent struct {
	Type         string
	ContentIndex int
	Delta        string
	Block        *cursorwire.OutBlock
}

// Projector is NeoPi's wrapLeakedThinkingStream (LeakedThinkingProjector),
// which every provider stream short of an official first-party endpoint
// passes through: visible text runs through a ThinkingInbandScanner with
// implied opens, so leaked reasoning becomes thinking blocks, while native
// thinking and tool calls are forwarded. The projector owns block
// boundaries: a text block stays open until another block starts or the
// stream ends.
//
// Content blocks here carry no text or thinking signatures, images or
// Anthropic server-tool history, so NeoPi's handling of those is not ported.
type Projector struct {
	out            func(StreamEvent)
	healer         *thinkingScanner
	content        []*cursorwire.OutBlock
	text, thinking int
	fedTextLengths map[int]int
	// activeTextSource is the source text block whose held healer output has
	// not crossed a content boundary, -1 for none.
	activeTextSource int
	sourceAnchors    map[*cursorwire.OutBlock]int
	toolBlocks       map[int]projectedTool
	thinkingBlocks   map[int]int
	thinkingOrder    []int
	pendingEnds      map[int]bool
	final            []*cursorwire.OutBlock
	terminal         bool
}

type projectedTool struct {
	index int
	block *cursorwire.OutBlock
}

// NewProjector returns a projector writing the healed stream to out.
func NewProjector(out func(StreamEvent)) *Projector {
	return &Projector{
		out:              out,
		healer:           &thinkingScanner{impliedOpen: true},
		text:             -1,
		thinking:         -1,
		fedTextLengths:   map[int]int{},
		activeTextSource: -1,
		sourceAnchors:    map[*cursorwire.OutBlock]int{},
		toolBlocks:       map[int]projectedTool{},
		thinkingBlocks:   map[int]int{},
		pendingEnds:      map[int]bool{},
	}
}

func (p *Projector) emit(event StreamEvent) {
	if p.out != nil {
		p.out(event)
	}
}

// Push consumes one event of the inner stream; source is the inner
// message's content as of that event.
func (p *Projector) Push(event StreamEvent, source []*cursorwire.OutBlock) {
	if p.terminal {
		return
	}
	block := func() *cursorwire.OutBlock {
		if event.ContentIndex >= 0 && event.ContentIndex < len(source) {
			return source[event.ContentIndex]
		}
		return nil
	}
	switch event.Type {
	case "start":
		p.emit(StreamEvent{Type: "start"})
	case "text_delta":
		p.textDelta(event.ContentIndex, event.Delta)
	case "thinking_delta":
		p.thinkingDelta(event.ContentIndex, event.Delta)
	case "thinking_end":
		var thinking *cursorwire.OutBlock
		if b := block(); b != nil && b.Type == "thinking" {
			thinking = b
		}
		p.thinkingEnd(event.ContentIndex, thinking)
	case "toolcall_start":
		if b := block(); b != nil && b.Type == "toolCall" {
			p.toolStart(event.ContentIndex, b)
		}
	case "toolcall_delta":
		var tool *cursorwire.OutBlock
		if b := block(); b != nil && b.Type == "toolCall" {
			tool = b
		}
		p.toolDelta(event.ContentIndex, event.Delta, tool)
	case "toolcall_end":
		p.toolEnd(event.ContentIndex, event.Block)
	case "done":
		p.terminal = true
		p.final = p.finish(source)
		p.emit(StreamEvent{Type: "done"})
	case "error":
		p.terminal = true
		// An error with empty content discarded the partial turn: forward it
		// as is rather than resurrect what was discarded.
		if len(source) == 0 {
			p.final = []*cursorwire.OutBlock{}
		} else {
			p.final = p.finish(source)
		}
		p.emit(StreamEvent{Type: "error"})
	}
	// text_start and thinking_start are ignored: the projector owns block
	// boundaries. text_end only carries a signature, which these blocks lack.
}

// Content is the healed message content once a terminal event has passed,
// nil before.
func (p *Projector) Content() []*cursorwire.OutBlock { return p.final }

func (p *Projector) textDelta(srcIndex int, delta string) {
	startsSource := p.activeTextSource != srcIndex
	if p.activeTextSource >= 0 && startsSource {
		p.flushHealer()
		p.closeText()
		p.closeThinking()
	}
	p.activeTextSource = srcIndex
	p.fedTextLengths[srcIndex] += len(delta)
	p.apply(p.healer.feed(delta), srcIndex)
}

func (p *Projector) thinkingDelta(srcIndex int, delta string) {
	index, ok := p.thinkingBlocks[srcIndex]
	if !ok {
		if p.thinking >= 0 && p.pendingEnds[p.thinking] {
			p.closeThinking()
		}
		index = p.openThinking(srcIndex)
		p.thinkingBlocks[srcIndex] = index
		p.thinkingOrder = append(p.thinkingOrder, srcIndex)
		p.pendingEnds[index] = true
	}
	p.content[index].Text += delta
	p.emit(StreamEvent{Type: "thinking_delta", ContentIndex: index, Delta: delta})
}

func (p *Projector) thinkingEnd(srcIndex int, source *cursorwire.OutBlock) {
	index, ok := p.thinkingBlocks[srcIndex]
	if !ok {
		return
	}
	p.appendThinkingTail(index, source)
	if !p.pendingEnds[index] {
		return
	}
	delete(p.pendingEnds, index)
	if p.thinking == index {
		p.thinking = -1
	}
	p.emit(StreamEvent{Type: "thinking_end", ContentIndex: index})
}

// appendThinkingTail emits native thinking the source finalized without
// streaming it as deltas.
func (p *Projector) appendThinkingTail(index int, source *cursorwire.OutBlock) {
	block := p.content[index]
	if source == nil || len(source.Text) <= len(block.Text) || !strings.HasPrefix(source.Text, block.Text) {
		return
	}
	delta := source.Text[len(block.Text):]
	block.Text += delta
	p.emit(StreamEvent{Type: "thinking_delta", ContentIndex: index, Delta: delta})
}

func (p *Projector) toolStart(srcIndex int, source *cursorwire.OutBlock) {
	p.flushHealer()
	p.closeText()
	p.closeThinking()
	block := cloneToolCall(source)
	p.content = append(p.content, block)
	index := len(p.content) - 1
	p.anchor(index, srcIndex)
	p.toolBlocks[srcIndex] = projectedTool{index: index, block: block}
	p.emit(StreamEvent{Type: "toolcall_start", ContentIndex: index, Block: block})
}

func (p *Projector) toolDelta(srcIndex int, delta string, source *cursorwire.OutBlock) {
	entry, ok := p.toolBlocks[srcIndex]
	if !ok && source != nil {
		p.toolStart(srcIndex, source)
		entry, ok = p.toolBlocks[srcIndex]
	}
	if !ok {
		return
	}
	if source != nil {
		syncToolCall(entry.block, source)
	}
	p.emit(StreamEvent{Type: "toolcall_delta", ContentIndex: entry.index, Delta: delta, Block: entry.block})
}

func (p *Projector) toolEnd(srcIndex int, toolCall *cursorwire.OutBlock) {
	if entry, ok := p.toolBlocks[srcIndex]; ok {
		if toolCall != nil {
			syncToolCall(entry.block, toolCall)
		}
		p.emit(StreamEvent{Type: "toolcall_end", ContentIndex: entry.index, Block: entry.block})
		delete(p.toolBlocks, srcIndex)
		return
	}
	if toolCall == nil {
		return
	}
	// An end without a start: release held text, then forward it whole.
	p.flushHealer()
	p.closeText()
	p.closeThinking()
	block := cloneToolCall(toolCall)
	p.content = append(p.content, block)
	index := len(p.content) - 1
	p.anchor(index, srcIndex)
	p.emit(StreamEvent{Type: "toolcall_start", ContentIndex: index, Block: block})
	p.emit(StreamEvent{Type: "toolcall_end", ContentIndex: index, Block: block})
}

// finish replays any visible-text tail the deltas did not carry, flushes
// held fragments, closes open blocks and returns the healed content in
// source order.
func (p *Projector) finish(source []*cursorwire.OutBlock) []*cursorwire.OutBlock {
	for _, srcIndex := range p.thinkingOrder {
		var thinking *cursorwire.OutBlock
		if srcIndex < len(source) && source[srcIndex].Type == "thinking" {
			thinking = source[srcIndex]
		}
		p.thinkingEnd(srcIndex, thinking)
	}
	for srcIndex, block := range source {
		if block.Type != "text" {
			continue
		}
		fed := p.fedTextLengths[srcIndex]
		if len(block.Text) <= fed {
			continue
		}
		if p.activeTextSource >= 0 && p.activeTextSource != srcIndex {
			p.flushHealer()
			p.closeText()
			p.closeThinking()
		}
		p.activeTextSource = srcIndex
		p.apply(p.healer.feed(block.Text[fed:]), srcIndex)
	}
	p.flushHealer()
	p.closeText()
	p.closeThinking()
	type anchored struct {
		block  *cursorwire.OutBlock
		source int
		order  int
	}
	ordered := make([]anchored, len(p.content))
	for order, block := range p.content {
		anchor, ok := p.sourceAnchors[block]
		if !ok {
			anchor = len(source) + order
		}
		ordered[order] = anchored{block: block, source: anchor, order: order}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].source != ordered[j].source {
			return ordered[i].source < ordered[j].source
		}
		return ordered[i].order < ordered[j].order
	})
	out := make([]*cursorwire.OutBlock, len(ordered))
	for i, entry := range ordered {
		out[i] = entry.block
	}
	return out
}

func (p *Projector) apply(events []scanEvent, srcIndex int) {
	for _, event := range events {
		switch event.kind {
		case scanText:
			p.emitText(event.text, srcIndex)
		case scanThinkingDelta:
			p.emitHealedThinking(event.text, srcIndex)
		case scanImpliedThinkingEnd:
			p.closeImpliedThinking(srcIndex)
		}
	}
}

// closeImpliedThinking handles a bare reasoning close with no open in this
// stream: when the open text block is the message's only content, it was
// reasoning behind a template-prefilled opener and becomes a closed thinking
// block at the same index; anywhere else the stray tag is dropped.
func (p *Projector) closeImpliedThinking(srcIndex int) {
	if p.text != 0 || len(p.content) != 1 {
		return
	}
	text := p.content[0].Text
	if jsTrim(text) == "" {
		return
	}
	p.closeText()
	block := &cursorwire.OutBlock{Type: "thinking", Text: text}
	p.content[0] = block
	p.anchor(0, srcIndex)
	p.emit(StreamEvent{Type: "thinking_start", ContentIndex: 0})
	p.emit(StreamEvent{Type: "thinking_delta", ContentIndex: 0, Delta: text})
	p.emit(StreamEvent{Type: "thinking_end", ContentIndex: 0})
}

func (p *Projector) emitText(text string, srcIndex int) {
	if text == "" {
		return
	}
	p.closeThinking()
	if p.text < 0 {
		p.content = append(p.content, &cursorwire.OutBlock{Type: "text"})
		p.text = len(p.content) - 1
		p.anchor(p.text, srcIndex)
		p.emit(StreamEvent{Type: "text_start", ContentIndex: p.text})
	}
	p.content[p.text].Text += text
	p.emit(StreamEvent{Type: "text_delta", ContentIndex: p.text, Delta: text})
}

// emitHealedThinking adds reasoning healed out of the visible text.
func (p *Projector) emitHealedThinking(text string, srcIndex int) {
	if text == "" {
		return
	}
	index := p.openThinking(srcIndex)
	p.content[index].Text += text
	p.emit(StreamEvent{Type: "thinking_delta", ContentIndex: index, Delta: text})
}

func (p *Projector) openThinking(srcIndex int) int {
	p.closeText()
	if p.thinking < 0 {
		p.content = append(p.content, &cursorwire.OutBlock{Type: "thinking"})
		p.thinking = len(p.content) - 1
		p.anchor(p.thinking, srcIndex)
		p.emit(StreamEvent{Type: "thinking_start", ContentIndex: p.thinking})
	}
	return p.thinking
}

func (p *Projector) flushHealer() {
	if p.activeTextSource >= 0 {
		p.apply(p.healer.flush(), p.activeTextSource)
	}
	p.activeTextSource = -1
}

func (p *Projector) anchor(index, srcIndex int) {
	p.sourceAnchors[p.content[index]] = srcIndex
}

func (p *Projector) closeText() {
	if p.text < 0 {
		return
	}
	p.emit(StreamEvent{Type: "text_end", ContentIndex: p.text})
	p.text = -1
}

func (p *Projector) closeThinking() {
	if p.thinking < 0 {
		return
	}
	index := p.thinking
	p.thinking = -1
	if p.pendingEnds[index] {
		return
	}
	p.emit(StreamEvent{Type: "thinking_end", ContentIndex: index})
}

func cloneToolCall(source *cursorwire.OutBlock) *cursorwire.OutBlock {
	return &cursorwire.OutBlock{Type: "toolCall", ID: source.ID, Name: source.Name, Arguments: source.Arguments}
}

func syncToolCall(target, source *cursorwire.OutBlock) {
	target.ID, target.Name, target.Arguments = source.ID, source.Name, source.Arguments
}

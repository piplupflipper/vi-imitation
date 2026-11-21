package server_test

import (
	"context"
	"strings"
	"testing"

	"github.com/piplupflipper/vi-imitation/ext"
	"github.com/piplupflipper/vi-imitation/ext/server"
)

// window/change を OnWindowChange のフックへ、窓のバッファの複製と一緒に渡し、SetBand が band/set を送る（FR-235、FR-237）。
func TestWindowChangeAndBand(t *testing.T) {
	s := server.New()
	type got struct {
		p    ext.WindowChangeParams
		text string
		ok   bool
	}
	events := make(chan got, 4)
	s.OnWindowChange(func(_ context.Context, e *server.WindowChangeEvent) {
		g := got{p: e.Params}
		if e.Buffer != nil {
			g.text, g.ok = e.Buffer.Text(), true
		}
		events <- g
		if !e.Params.Closed {
			// フックの中から帯を送れる（応答を待たない）。
			_ = s.SetBand(ext.BandSetParams{Window: e.Params.Window, Buffer: e.Params.Buffer, Version: e.Params.Version,
				Lines: []ext.BandLine{{Text: "func f() {", Line: ext.SourceLine(0)}}})
		}
	})
	h := start(t, s)
	h.notify(t, ext.MethodBufferOpen, ext.BufferOpenParams{Buffer: 1, Version: 1, Text: "func f() {\n}"})
	h.notify(t, ext.MethodWindowChange, ext.WindowChangeParams{Window: "client/1", Buffer: 1, Version: 1, Top: 1, Bottom: 2, Cursor: ext.Position{Line: 1}})
	g := <-events
	if g.p.Window != "client/1" || g.p.Top != 1 || !g.ok || g.text != "func f() {\n}" {
		t.Errorf("event = %+v", g)
	}
	n := h.nextNote(t)
	if n.Method != ext.MethodBandSet || !strings.Contains(string(n.Params), `"line":0`) {
		t.Errorf("got %s %s", n.Method, n.Params)
	}
	// 持っていないバッファの窓と、閉じた窓では、複製は nil である。
	h.notify(t, ext.MethodWindowChange, ext.WindowChangeParams{Window: "client/2", Buffer: 9})
	if g := <-events; g.ok {
		t.Errorf("持っていないバッファの複製 = %+v", g)
	}
	h.nextNote(t)
	h.notify(t, ext.MethodWindowChange, ext.WindowChangeParams{Window: "client/2", Closed: true, Buffer: 1})
	if g := <-events; g.ok || !g.p.Closed {
		t.Errorf("閉じた窓 = %+v", g)
	}
	h.noNote(t)
}

// SetBand は、検査を通らない帯を送らずに誤りを返し、空の帯は行の空の列で送る（本体はそれで帯を消す）。
func TestSetBand(t *testing.T) {
	s := server.New()
	h := start(t, s)
	h.ping(t)
	if err := s.SetBand(ext.BandSetParams{Window: "client/1", Lines: []ext.BandLine{{Text: "a\nb"}}}); err == nil {
		t.Error("改行を含む帯を送った")
	}
	if err := s.SetBand(ext.BandSetParams{Window: "client/1"}); err != nil {
		t.Fatal(err)
	}
	if n := h.nextNote(t); n.Method != ext.MethodBandSet || !strings.Contains(string(n.Params), `"lines":[]`) {
		t.Errorf("got %s %s", n.Method, n.Params)
	}
	h.noNote(t)
}

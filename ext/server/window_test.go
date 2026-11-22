package server_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

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
			_ = s.SetBand(ext.BandSetParams{
				Window: e.Params.Window, Buffer: e.Params.Buffer, Version: e.Params.Version,
				Lines: []ext.BandLine{{Text: "func f() {", Line: ext.SourceLine(0)}},
			})
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

// フックが前の知らせを処理している間に、同じ窓の知らせが続けて届いたら、最後の 1 件だけをフックへ渡す。窓ごとに分け、順序は保つ。
//
// req: FR-236
func TestWindowChangeCoalesce(t *testing.T) {
	s := server.New()
	release := make(chan struct{})
	got := make(chan ext.WindowChangeParams, 16)
	s.OnWindowChange(func(_ context.Context, e *server.WindowChangeEvent) {
		got <- e.Params
		if e.Params.Seq == 1 {
			<-release // 最初の知らせの処理を遅らせる
		}
	})
	h := start(t, s)
	h.notify(t, ext.MethodWindowChange, ext.WindowChangeParams{Window: "client/1", Seq: 1})
	if p := <-got; p.Seq != 1 {
		t.Fatalf("最初の知らせ = %+v", p)
	}
	for i := uint64(2); i <= 5; i++ {
		h.notify(t, ext.MethodWindowChange, ext.WindowChangeParams{Window: "client/1", Top: int(i), Seq: i})
	}
	h.notify(t, ext.MethodWindowChange, ext.WindowChangeParams{Window: "client/2", Seq: 6})
	h.notify(t, ext.MethodWindowChange, ext.WindowChangeParams{Window: "client/1", Top: 7, Seq: 7})
	// 読み取りの goroutine が全部を受け取ってから、フックを進める（ping の応答はループが返すので、ここでは待てない）。
	deadline := time.Now().Add(timeout)
	for s.WindowsArrived() < 7 {
		if time.Now().After(deadline) {
			t.Fatalf("届いた知らせ = %d 件", s.WindowsArrived())
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	h.ping(t)
	close(got)
	var seqs []uint64
	for p := range got {
		seqs = append(seqs, p.Seq)
	}
	if want := []uint64{6, 7}; !slices.Equal(seqs, want) {
		t.Errorf("フックへ渡した知らせの Seq = %v, want %v", seqs, want)
	}
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

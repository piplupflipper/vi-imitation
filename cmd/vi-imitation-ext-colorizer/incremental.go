package main

// longLineBytes は、編集の周りだけを数え直す行の長さの下限（仮に 64 KiB）である。
// これより短い行は、編集のたびに行の全体を数え直す（1 行が 64 KiB で 1.4 ms ほど。BenchmarkScanLine の 45 MB/s から）。
// minified の JS や CSS のように 1 行が数 MB から数十 MB の行は、行の全体を数え直すと 1 文字ごとに数十から数百 ms かかるので、
// 前に数えた範囲を使い、編集の周りだけを数え直す（rescanAround）。
const longLineBytes = 64 << 10

// rescanAround は、行 line の範囲を、編集の前の行の範囲 base から、編集の周りだけを数え直して求める。
// 編集は、前の行のバイトの位置 [start, oldEnd) を、line の [start, newEnd) に置き換えたものである。
// 結果は、line を scanLine で数えた結果と同じになる（TestRescanAroundMatchesScanLine）。
//
// 数え直す範囲は次のとおりに決める。scanner は、位置 i で、前の 1 バイトと、後ろの maxCodeBytes バイトまで
// （色の名前は英字と数字の並びの全体だが、nameMax より長い並びは名前と認めない）しか読まない。
//
//   - 始め: 編集の始まりから maxCodeBytes だけ前の位置 ws。ws より前で終わる前の範囲は、読んだ文字が変わらないので残す。
//     ws をまたぐ前の範囲があれば、その始まりから数え直す（前の数え方もその位置を試したので、同じ位置から始められる）
//   - 終わり: 編集の後ろ（newEnd より後）の位置 i で、i - (newEnd - oldEnd) が前の範囲の内側でなければ、前の数え方も
//     同じ文字の並びでその位置を試したので、そこから後ろは前の範囲をずらしたものと同じになる
func (s *scanner) rescanAround(line string, base []span, start, oldEnd, newEnd int) []span {
	delta := newEnd - oldEnd
	ws := max(0, start-maxCodeBytes)
	kept := 0
	for kept < len(base) && base[kept].end <= ws {
		kept++
	}
	if kept < len(base) && base[kept].start < ws {
		ws = base[kept].start
	}
	out := append([]span(nil), base[:kept]...)
	bi := kept
	for i := ws; i < len(line)-1; {
		if i > newEnd {
			y := i - delta
			for bi < len(base) && base[bi].end <= y {
				bi++
			}
			if bi == len(base) || base[bi].start >= y {
				for _, sp := range base[bi:] {
					out = append(out, span{start: sp.start + delta, end: sp.end + delta, rgb: sp.rgb})
				}
				return out
			}
		}
		n, rgb, ok := s.at(line, i)
		if !ok {
			i++
			continue
		}
		out = append(out, span{start: i, end: i + n, rgb: rgb})
		i += n
	}
	return out
}

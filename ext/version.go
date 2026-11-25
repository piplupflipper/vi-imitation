package ext

import (
	"slices"
	"strconv"
	"strings"
)

// VersionLess は、"major.minor.patch" の形の拡張機能 API の版 a が b より古いかを返す。
// 読めない a は古いものとして扱い（true）、a が読めて b が読めなければ古くないものとして扱う（false）。
//
// 拡張機能は、initialize の apiVersion（InitializeParams.APIVersion）を、要る版（拡張機能ごとの定数 minAPIVersion）と比べるのに使う
// （基本設計 1.4 の「古い本体での標準の拡張機能」）。比べ方が拡張機能ごとに食い違わないよう、比べる関数はここだけに置く。
func VersionLess(a, b string) bool {
	pa, oka := parseVersion(a)
	pb, okb := parseVersion(b)
	if !oka {
		return true
	}
	if !okb {
		return false
	}
	return slices.Compare(pa[:], pb[:]) < 0
}

// parseVersion は、"major.minor.patch" の 3 つの負でない整数を読む。
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

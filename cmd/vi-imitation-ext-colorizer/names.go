package main

//go:generate go run ./internal/namegen

// colorName は、色の名前の表（names_table.go の colorNames）の 1 つの項目である。
//
// 表は、Neovim の nvim_get_color_map() の名前と色を写したもので、nvim-colorizer.lua の色の名前（names）と同じ表である。
// 中身は Neovim（https://github.com/neovim/neovim、タグ v0.12.5。Apache License 2.0）の src/nvim/highlight_group.c の
// color_name_table で、X11 の rgb.txt の色と、Neovim の既定の配色の色（NvimDarkGrey1 など）からなる。
// ライセンスの文面は licenses/ にあり、配布物の licenses/ に入れる（Makefile の dist）。
// 作り直し方は internal/namegen の doc にある。
type colorName struct {
	name string
	rgb  uint32
}

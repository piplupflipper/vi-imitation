package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/piplupflipper/vi-imitation/ext"
)

// settings は、利用者の設定のうち、この拡張機能に向けた部分（extensions.settings.colorizer）である（基本設計 6.12）。
// 項目の名前は仮で、nvim-colorizer.lua の options の名前（display.mode、display.background、parsers）に寄せた。
// 知らない項目は誤りにする（綴りの誤りで形が黙って変わらないのを避ける。整形ツールの拡張機能と同じ）。
type settings struct {
	// FileTypes は、色を塗るファイルの種類の名前の列である。宣言ファイルの起動の契機もここから作る（settingsFileType）。
	// 書かなければ、この拡張機能は起動しない。
	FileTypes []string `json:"fileTypes"`
	// Mode は、塗り方である。background（背景をその色にし、文字を読める色にする）、foreground（文字をその色にする）、
	// underline（その色の下線を引く）のどれかで、省くと background（nvim-colorizer.lua の display.mode の既定）である。
	Mode string `json:"mode,omitempty"`
	// Background は、background の塗り方の文字の色である。
	Background *backgroundSettings `json:"background,omitempty"`
	// Hex は、# と 0x で始まる形を認識するかである。省いた項目は既定（formats の doc）のままにする。
	Hex *hexSettings `json:"hex,omitempty"`
	// RGB は rgb() と rgba()、HSL は hsl() と hsla() を認識するかである。省くと false である。
	RGB *bool `json:"rgb,omitempty"`
	HSL *bool `json:"hsl,omitempty"`
	// Names は、色の名前を認識するかである。省いた項目は既定のままにする。
	Names *namesSettings `json:"names,omitempty"`
}

// backgroundSettings は、background の塗り方で、明るい色と暗い色の上の文字の色である（nvim-colorizer.lua の display.background）。
type backgroundSettings struct {
	// BrightFg は明るい色の上の文字の色で、省くと #000000 である。DarkFg は暗い色の上の文字の色で、省くと #ffffff である。
	BrightFg string `json:"brightFg,omitempty"`
	DarkFg   string `json:"darkFg,omitempty"`
}

// hexSettings は、# と 0x で始まる形ごとに、認識するかである（nvim-colorizer.lua の parsers.hex）。
type hexSettings struct {
	RGB      *bool `json:"rgb,omitempty"`      // #RGB。既定は true
	RGBA     *bool `json:"rgba,omitempty"`     // #RGBA。既定は true
	RRGGBB   *bool `json:"rrggbb,omitempty"`   // #RRGGBB。既定は true
	RRGGBBAA *bool `json:"rrggbbaa,omitempty"` // #RRGGBBAA。既定は false
	AARRGGBB *bool `json:"aarrggbb,omitempty"` // 0xRGB、0xRRGGBB、0xAARRGGBB。既定は false
}

// namesSettings は、色の名前を認識するかである（nvim-colorizer.lua の parsers.names）。
type namesSettings struct {
	// Enable は、色の名前を認識するかで、既定は true である。false なら、ほかの項目によらず認識しない。
	Enable *bool `json:"enable,omitempty"`
	// Lowercase は小文字にした綴り（lightblue）、Camelcase は表のままの綴り（LightBlue）、Uppercase は大文字にした綴り（LIGHTBLUE）を
	// 認識するかで、既定は true、true、false である。
	Lowercase *bool `json:"lowercase,omitempty"`
	Camelcase *bool `json:"camelcase,omitempty"`
	Uppercase *bool `json:"uppercase,omitempty"`
}

// paintMode は、塗り方である。
type paintMode int

const (
	modeBackground paintMode = iota
	modeForeground
	modeUnderline
)

// config は、settings を検査して既定値を埋めたものである。作った後は変えない。
type config struct {
	fileTypes []string
	mode      paintMode
	// brightFg と darkFg は、background の塗り方の文字の色（#rrggbb）である。
	brightFg, darkFg string
	formats          formats
}

// parseSettings は、initialize の settings を読んで検査する。settings が無ければ、ファイルの種類の無い既定の設定を返す。
// 誤りはすべて 1 つの誤りにまとめて返す。
func parseSettings(raw json.RawMessage) (*config, error) {
	c := &config{mode: modeBackground, brightFg: "#000000", darkFg: "#ffffff", formats: defaultFormats()}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return c, nil
	}
	var s settings
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("extensions.settings.colorizer を読めない: %w", err)
	}
	var errs []error
	if slices.Contains(s.FileTypes, "") {
		errs = append(errs, errors.New("extensions.settings.colorizer.fileTypes に空の名前がある"))
	}
	c.fileTypes = s.FileTypes
	switch s.Mode {
	case "", "background":
	case "foreground":
		c.mode = modeForeground
	case "underline":
		c.mode = modeUnderline
	default:
		errs = append(errs, fmt.Errorf("extensions.settings.colorizer.mode %q は background、foreground、underline のどれかにする", s.Mode))
	}
	if bg := s.Background; bg != nil {
		for _, f := range []struct {
			name string
			v    string
			dst  *string
		}{{"brightFg", bg.BrightFg, &c.brightFg}, {"darkFg", bg.DarkFg, &c.darkFg}} {
			if f.v == "" {
				continue
			}
			if !ext.IsHexColor(f.v) {
				errs = append(errs, fmt.Errorf("extensions.settings.colorizer.background.%s %q は #rrggbb の形にする", f.name, f.v))
				continue
			}
			*f.dst = f.v
		}
	}
	set := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	f := &c.formats
	if h := s.Hex; h != nil {
		set(&f.hexRGB, h.RGB)
		set(&f.hexRGBA, h.RGBA)
		set(&f.hexRRGGBB, h.RRGGBB)
		set(&f.hexRRGGBBAA, h.RRGGBBAA)
		set(&f.hexAARRGGBB, h.AARRGGBB)
	}
	set(&f.rgbFn, s.RGB)
	set(&f.hslFn, s.HSL)
	if n := s.Names; n != nil {
		set(&f.namesLower, n.Lowercase)
		set(&f.namesCamel, n.Camelcase)
		set(&f.namesUpper, n.Uppercase)
		if n.Enable != nil && !*n.Enable {
			f.namesLower, f.namesCamel, f.namesUpper = false, false, false
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

// targets は、ファイルの種類 ft のバッファに色を塗るかを返す。
func (c *config) targets(ft string) bool {
	return ft != "" && slices.Contains(c.fileTypes, ft)
}

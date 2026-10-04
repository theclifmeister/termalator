package main

import (
	uv "github.com/charmbracelet/ultraviolet"
	"go.mitchellh.com/libghostty"
)

var specialKeys = map[rune]libghostty.Key{
	uv.KeyEnter: libghostty.KeyEnter, uv.KeyTab: libghostty.KeyTab,
	uv.KeyBackspace: libghostty.KeyBackspace, uv.KeyEscape: libghostty.KeyEscape,
	uv.KeySpace: libghostty.KeySpace,
	uv.KeyUp:    libghostty.KeyArrowUp, uv.KeyDown: libghostty.KeyArrowDown,
	uv.KeyLeft: libghostty.KeyArrowLeft, uv.KeyRight: libghostty.KeyArrowRight,
	uv.KeyHome: libghostty.KeyHome, uv.KeyEnd: libghostty.KeyEnd,
	uv.KeyPgUp: libghostty.KeyPageUp, uv.KeyPgDown: libghostty.KeyPageDown,
	uv.KeyInsert: libghostty.KeyInsert, uv.KeyDelete: libghostty.KeyDelete,
	uv.KeyKpEnter: libghostty.KeyNumpadEnter,
	uv.KeyF1:      libghostty.KeyF1, uv.KeyF2: libghostty.KeyF2, uv.KeyF3: libghostty.KeyF3,
	uv.KeyF4: libghostty.KeyF4, uv.KeyF5: libghostty.KeyF5, uv.KeyF6: libghostty.KeyF6,
	uv.KeyF7: libghostty.KeyF7, uv.KeyF8: libghostty.KeyF8, uv.KeyF9: libghostty.KeyF9,
	uv.KeyF10: libghostty.KeyF10, uv.KeyF11: libghostty.KeyF11, uv.KeyF12: libghostty.KeyF12,
}

var printableKeys = func() map[rune]libghostty.Key {
	m := map[rune]libghostty.Key{
		'`': libghostty.KeyBackquote, '\\': libghostty.KeyBackslash,
		'[': libghostty.KeyBracketLeft, ']': libghostty.KeyBracketRight,
		',': libghostty.KeyComma, '=': libghostty.KeyEqual, '-': libghostty.KeyMinus,
		'.': libghostty.KeyPeriod, '\'': libghostty.KeyQuote, ';': libghostty.KeySemicolon,
		'/': libghostty.KeySlash,
	}
	letters := []libghostty.Key{libghostty.KeyA, libghostty.KeyB, libghostty.KeyC, libghostty.KeyD,
		libghostty.KeyE, libghostty.KeyF, libghostty.KeyG, libghostty.KeyH, libghostty.KeyI,
		libghostty.KeyJ, libghostty.KeyK, libghostty.KeyL, libghostty.KeyM, libghostty.KeyN,
		libghostty.KeyO, libghostty.KeyP, libghostty.KeyQ, libghostty.KeyR, libghostty.KeyS,
		libghostty.KeyT, libghostty.KeyU, libghostty.KeyV, libghostty.KeyW, libghostty.KeyX,
		libghostty.KeyY, libghostty.KeyZ}
	for i, k := range letters {
		m['a'+rune(i)] = k
	}
	digits := []libghostty.Key{libghostty.KeyDigit0, libghostty.KeyDigit1, libghostty.KeyDigit2,
		libghostty.KeyDigit3, libghostty.KeyDigit4, libghostty.KeyDigit5, libghostty.KeyDigit6,
		libghostty.KeyDigit7, libghostty.KeyDigit8, libghostty.KeyDigit9}
	for i, k := range digits {
		m['0'+rune(i)] = k
	}
	return m
}()

// fillKeyEvent translates a key decoded from the outer terminal into a
// libghostty key event, which is then re-encoded for the inner program
// according to the modes *it* negotiated (legacy, DECCKM, kitty flags...).
func fillKeyEvent(ev *libghostty.KeyEvent, k uv.Key, action libghostty.KeyAction) {
	var mods libghostty.Mods
	if k.Mod.Contains(uv.ModShift) {
		mods |= libghostty.ModShift
	}
	if k.Mod.Contains(uv.ModCtrl) {
		mods |= libghostty.ModCtrl
	}
	if k.Mod.Contains(uv.ModAlt) || k.Mod.Contains(uv.ModMeta) {
		mods |= libghostty.ModAlt
	}
	if k.Mod.Contains(uv.ModSuper) {
		mods |= libghostty.ModSuper
	}
	key := libghostty.KeyUnidentified
	var unshifted rune
	if sk, ok := specialKeys[k.Code]; ok {
		key = sk
		if k.Code == uv.KeySpace {
			unshifted = ' '
		}
	} else if pk, ok := printableKeys[k.Code]; ok {
		key = pk
		unshifted = k.Code
	} else if k.Code < uv.KeyExtended {
		unshifted = k.Code
	}
	text := k.Text
	if mods&(libghostty.ModCtrl|libghostty.ModAlt|libghostty.ModSuper) != 0 {
		text = "" // the encoder derives control/alt sequences itself
	}
	var consumed libghostty.Mods
	if text != "" && mods&libghostty.ModShift != 0 {
		consumed = libghostty.ModShift
	}
	ev.SetAction(action)
	ev.SetKey(key)
	ev.SetMods(mods)
	ev.SetConsumedMods(consumed)
	ev.SetUTF8(text)
	ev.SetUnshiftedCodepoint(unshifted)
}

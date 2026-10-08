//go:build !cgo

package emu

import "errors"

// Without cgo there is no libghostty-vt: every constructor fails with
// ErrNoCgo and the methods do nothing. This build only exists so that
// `CGO_ENABLED=0 GOOS=<any> go vet ./...` type-checks the whole tree
// without Zig; a real tm always builds with cgo.

// ErrNoCgo is what every emulator call returns in a build without cgo.
var ErrNoCgo = errors.New("emu: built without cgo (no libghostty-vt)")

// Terminal is one emulated screen (a stub without cgo).
type Terminal struct{}

func SchemeReport(Scheme) []byte                                 { return nil }
func New(cols, rows uint16) (*Terminal, error)                   { return nil, ErrNoCgo }
func NewWith(Options) (*Terminal, error)                         { return nil, ErrNoCgo }
func Decode(snapshot []byte) (*Terminal, error)                  { return nil, ErrNoCgo }
func (t *Terminal) Write(p []byte) (int, error)                  { return 0, ErrNoCgo }
func (t *Terminal) Resize(cols, rows uint16) error               { return ErrNoCgo }
func (t *Terminal) Size() (cols, rows uint16)                    { return 0, 0 }
func (t *Terminal) Title() string                                { return "" }
func (t *Terminal) Snapshot() ([]byte, error)                    { return nil, ErrNoCgo }
func (t *Terminal) VT() ([]byte, error)                          { return nil, ErrNoCgo }
func (t *Terminal) Digest() (string, error)                      { return "", ErrNoCgo }
func (t *Terminal) PlainText() (string, error)                   { return "", ErrNoCgo }
func (t *Terminal) Screen() (string, error)                      { return "", ErrNoCgo }
func (t *Terminal) Close()                                       {}
func (t *Terminal) Rows() (plain, noDim []string, err error)     { return nil, nil, ErrNoCgo }
func (t *Terminal) Select(x0, y0, x1, y1 int) error              { return ErrNoCgo }
func (t *Terminal) ClearSelection() error                        { return ErrNoCgo }
func (t *Terminal) SelectionText() (string, error)               { return "", ErrNoCgo }
func (t *Terminal) OnClipboard(fn func(which byte, data []byte)) {}
func (t *Terminal) Modes() Modes                                 { return Modes{} }
func (t *Terminal) ScrollViewport(delta int)                     {}
func (t *Terminal) ScrollViewportBottom()                        {}
func (t *Terminal) OnRenderHold(fn func(held bool))              {}

// Encoder encodes input for one terminal (a stub without cgo).
type Encoder struct{}

func NewEncoder() (*Encoder, error)                           { return nil, ErrNoCgo }
func (e *Encoder) Close()                                     {}
func (e *Encoder) Key(t *Terminal, k Key) ([]byte, error)     { return nil, ErrNoCgo }
func (e *Encoder) Mouse(t *Terminal, m Mouse) ([]byte, error) { return nil, ErrNoCgo }
func Paste(t *Terminal, text []byte) ([]byte, error)          { return nil, ErrNoCgo }
func Focus(t *Terminal, gained bool) []byte                   { return nil }

// Renderer draws a Terminal onto an outer terminal (a stub without cgo).
type Renderer struct{}

func NewRenderer(cols, rows uint16) (*Renderer, error)           { return nil, ErrNoCgo }
func (r *Renderer) Close()                                       {}
func (r *Renderer) SetSize(cols, rows uint16)                    {}
func (r *Renderer) SetRect(x, y, cols, rows int)                 {}
func (r *Renderer) Cursor() []byte                               { return nil }
func (r *Renderer) SetStatus(line string)                        {}
func (r *Renderer) Invalidate()                                  {}
func (r *Renderer) Top() int                                     { return 0 }
func (r *Renderer) Capture(t *Terminal) error                    { return ErrNoCgo }
func (r *Renderer) Frame(t *Terminal, held bool) ([]byte, error) { return nil, ErrNoCgo }

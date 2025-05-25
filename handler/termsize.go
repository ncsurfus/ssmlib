package handler

const DefaultTerminalWidth = 80
const DefaultTerminalHeight = 24

type TerminalSize interface {
	Size() (width int, height int, err error)
}

type TerminalSizeFunc func() (width int, height int, err error)

func (gsf TerminalSizeFunc) Size() (width int, height int, err error) {
	return gsf()
}

var DefaultTerminalSizeFunc = TerminalSizeFunc(func() (width int, height int, err error) {
	return DefaultTerminalWidth, DefaultTerminalHeight, nil
})

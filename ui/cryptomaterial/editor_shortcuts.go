package cryptomaterial

import "gioui.org/io/key"

// nonLatinShortcut maps the uppercase letter Gio reports for a
// Russian/Ukrainian JCUKEN layout back to the Latin editing shortcut.
// Gio names the key from the active layout, while widget.Editor only
// matches A/C/V/X/Z.
var nonLatinShortcut = map[string]string{
	"Ф": "A", // select all
	"С": "C", // copy
	"М": "V", // paste
	"Ч": "X", // cut
	"Я": "Z", // undo / redo
}

func latinShortcut(name string) (string, bool) {
	latin, ok := nonLatinShortcut[name]
	return latin, ok
}

// historyShortcutFilter matches Latin Cmd/Ctrl+Z (Shift = redo). It is
// read before widget.Editor.Update, so Latin and non-Latin undo share
// Editor.undoStack and widget.Editor's own history is never used.
func historyShortcutFilter(tag any) key.Filter {
	return key.Filter{
		Focus:    tag,
		Name:     "Z",
		Required: key.ModShortcut,
		Optional: key.ModShift,
	}
}

func nonLatinShortcutFilters(tag any) []key.Filter {
	filters := make([]key.Filter, 0, len(nonLatinShortcut))
	for name := range nonLatinShortcut {
		filters = append(filters, key.Filter{
			Focus:    tag,
			Name:     key.Name(name),
			Required: key.ModShortcut,
			Optional: key.ModShift,
		})
	}
	return filters
}

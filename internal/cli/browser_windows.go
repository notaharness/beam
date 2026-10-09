package cli

import "golang.org/x/sys/windows"

// desktop is the user's default browser, which the shell opens a URL with.
func desktop(func(string) string) func(url string) error { return shellOpen }

func shellOpen(url string) error {
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(url)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL)
}

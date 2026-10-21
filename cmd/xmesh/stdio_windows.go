package main

import "os"

// A GUI-subsystem task starts with no console and may have invalid standard
// handles. Keep inherited CLI pipes, but provide NUL for missing streams so a
// Computer Host does not exit while publishing its startup metadata.
func prepareStandardIO() {
	for _, stream := range []struct {
		file **os.File
		mode int
	}{
		{&os.Stdin, os.O_RDONLY},
		{&os.Stdout, os.O_WRONLY},
		{&os.Stderr, os.O_WRONLY},
	} {
		if _, err := (*stream.file).Stat(); err != nil {
			if file, err := os.OpenFile(os.DevNull, stream.mode, 0); err == nil {
				*stream.file = file
			}
		}
	}
}

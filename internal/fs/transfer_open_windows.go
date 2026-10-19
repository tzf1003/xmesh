package fs

import "os"

func openTransferRead(path string) (*os.File, error) { return openRead(path) }

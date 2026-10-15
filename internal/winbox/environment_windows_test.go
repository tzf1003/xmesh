package winbox

import (
	"reflect"
	"testing"

	"golang.org/x/sys/windows"
)

func TestEnvironment(t *testing.T) {
	local, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("XMESH_WINBOX_PARENT_SECRET", "sentinel")
	for _, input := range [][]string{nil, {"LOCALAPPDATA=C:\\wrong"}, {"localappdata=C:\\wrong", "LocalAppData=C:\\other"}} {
		input = append([]string{"SystemRoot=C:\\Windows"}, input...)
		before := append([]string(nil), input...)
		got, err := Environment(input)
		want := []string{"LOCALAPPDATA=" + local, "SystemRoot=C:\\Windows"}
		if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(input, before) {
			t.Fatal("AppContainer environment did not retain the filtered entries and canonical profile location", err)
		}
	}
	for _, input := range [][]string{{"MALFORMED"}, {"=value"}, {"NAME=embedded\x00value"}} {
		if _, err := Environment(input); err == nil {
			t.Fatal("accepted malformed environment")
		}
	}
}

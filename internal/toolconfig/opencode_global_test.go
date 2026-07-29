package toolconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeOpencodeGlobalFixture(t *testing.T, home, content string) string {
	t.Helper()
	path := DefaultConfigPath(ToolOpencode, home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func boolPtr(value bool) *bool {
	return &value
}

func TestReadOpencodeGlobalSettings(t *testing.T) {
	empty, err := ReadOpencodeGlobalSettings(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if empty != (OpencodeGlobalSettings{}) {
		t.Fatalf("missing settings = %+v", empty)
	}

	home := t.TempDir()
	writeOpencodeGlobalFixture(t, home, `{
  "model": "provider/model",
  "small_model": "provider/small",
  "theme": "system",
  "share": "manual",
  "autoupdate": false
}`)
	settings, err := ReadOpencodeGlobalSettings(home)
	if err != nil {
		t.Fatal(err)
	}
	want := OpencodeGlobalSettings{Model: "provider/model", SmallModel: "provider/small", Theme: "system", Share: "manual", Autoupdate: boolPtr(false)}
	if settings.Model != want.Model || settings.SmallModel != want.SmallModel || settings.Theme != want.Theme || settings.Share != want.Share || settings.Autoupdate == nil || *settings.Autoupdate != *want.Autoupdate {
		t.Fatalf("settings = %+v, want %+v", settings, want)
	}

	writeOpencodeGlobalFixture(t, home, `{"autoupdate":"yes"}`)
	settings, err = ReadOpencodeGlobalSettings(home)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Autoupdate != nil {
		t.Fatalf("non-bool autoupdate = %#v, want unset", settings.Autoupdate)
	}
}

func TestPlanOpencodeGlobalChangeSetAndRemovesOwnedLeaves(t *testing.T) {
	home := t.TempDir()
	path := writeOpencodeGlobalFixture(t, home, `{
  // retain this comment
  "model": "old/model",
  "unmanaged": {"keep": true},
  "small_model": "old/small",
  "theme": "old",
  "share": "manual",
  "autoupdate": true
}`)
	setAll := OpencodeGlobalSettings{
		Model:      "new/model",
		SmallModel: "new/small",
		Theme:      "new",
		Share:      "auto",
		Autoupdate: boolPtr(false),
	}
	changeSet, err := PlanOpencodeGlobalChange(home, setAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(changeSet.Changes) != 1 || string(changeSet.Changes[0].Before) != string(mustReadFile(t, path)) {
		t.Fatalf("unexpected snapshot: %+v", changeSet.Changes)
	}
	after := string(changeSet.Changes[0].After)
	for _, value := range []string{"new/model", "new/small", `"theme"`, `"share"`, `"autoupdate"`, "retain this comment", `"unmanaged"`} {
		if !strings.Contains(after, value) {
			t.Errorf("after output missing %q: %s", value, after)
		}
	}

	removeAll, err := PlanOpencodeGlobalChange(home, OpencodeGlobalSettings{})
	if err != nil {
		t.Fatal(err)
	}
	removed := string(removeAll.Changes[0].After)
	for _, key := range []string{"model", "small_model", "theme", "share", "autoupdate"} {
		if strings.Contains(removed, `"`+key+`"`) {
			t.Errorf("owned key %q survived removal: %s", key, removed)
		}
	}
	if !strings.Contains(removed, "retain this comment") || !strings.Contains(removed, `"unmanaged": {"keep": true}`) {
		t.Fatalf("unmanaged content was not preserved: %s", removed)
	}
}

func TestPlanOpencodeGlobalChangePartialAndAutoupdateTriState(t *testing.T) {
	home := t.TempDir()
	writeOpencodeGlobalFixture(t, home, `{"model":"old/model","theme":"old","autoupdate":true,"other":1}`)
	partial, err := PlanOpencodeGlobalChange(home, OpencodeGlobalSettings{Theme: "new", Autoupdate: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	partialText := string(partial.Changes[0].After)
	if !strings.Contains(partialText, `"theme"`) || !strings.Contains(partialText, `"new"`) || !strings.Contains(partialText, `"autoupdate"`) || !strings.Contains(partialText, `false`) || strings.Contains(partialText, `"model"`) {
		t.Fatalf("partial output = %s", partialText)
	}

	removed, err := PlanOpencodeGlobalChange(home, OpencodeGlobalSettings{Autoupdate: nil})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(removed.Changes[0].After), `"autoupdate"`) {
		t.Fatalf("nil autoupdate did not remove key: %s", removed.Changes[0].After)
	}
}

func TestPlanOpencodeGlobalChangeFailsClosedOnRootShapeAndDuplicate(t *testing.T) {
	cases := []struct {
		name string
		data string
		want error
	}{
		{name: "root array", data: `[]`, want: ErrUnsafeShape},
		{name: "duplicate root key", data: `{"model":"a","model":"b"}`, want: ErrUnsafeShape},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeOpencodeGlobalFixture(t, home, tc.data)
			_, err := PlanOpencodeGlobalChange(home, OpencodeGlobalSettings{Model: "replacement"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}

	home := t.TempDir()
	writeOpencodeGlobalFixture(t, home, `{"model":123,"other":true}`)
	changeSet, err := PlanOpencodeGlobalChange(home, OpencodeGlobalSettings{Model: "replacement"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(changeSet.Changes[0].After), `"model"`) || !strings.Contains(string(changeSet.Changes[0].After), "replacement") {
		t.Fatalf("owned wrong-type leaf was not overwritten: %s", changeSet.Changes[0].After)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

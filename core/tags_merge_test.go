package core

import "testing"

func TestMergeFileAndInlineTags(t *testing.T) {
	t.Run("file first then inline", func(t *testing.T) {
		dc := &DriverConfig{Name: "d", Tags: []TagConfig{{Name: "inline"}}}
		err := MergeFileAndInlineTags(dc, []TagConfig{{Name: "file"}})
		if err != nil {
			t.Fatalf("merge: %v", err)
		}
		if len(dc.Tags) != 2 || dc.Tags[0].Name != "file" || dc.Tags[1].Name != "inline" {
			t.Fatalf("merge order = %+v, want [file inline]", dc.Tags)
		}
		// InlineTags keeps only what the operator authored.
		if len(dc.InlineTags) != 1 || dc.InlineTags[0].Name != "inline" {
			t.Fatalf("InlineTags = %+v, want [inline]", dc.InlineTags)
		}
	})

	t.Run("duplicate inside the file is rejected", func(t *testing.T) {
		dc := &DriverConfig{Name: "d"}
		err := MergeFileAndInlineTags(dc, []TagConfig{{Name: "a"}, {Name: "a"}})
		if err == nil {
			t.Fatal("duplicate file tags must be rejected")
		}
	})

	t.Run("duplicate across file and inline is rejected", func(t *testing.T) {
		dc := &DriverConfig{Name: "d", Tags: []TagConfig{{Name: "same"}}}
		err := MergeFileAndInlineTags(dc, []TagConfig{{Name: "same"}})
		if err == nil {
			t.Fatal("file/inline collision must be rejected")
		}
	})

	t.Run("nil file tags keeps inline only", func(t *testing.T) {
		dc := &DriverConfig{Name: "d", Tags: []TagConfig{{Name: "inline"}}}
		if err := MergeFileAndInlineTags(dc, nil); err != nil {
			t.Fatalf("merge: %v", err)
		}
		if len(dc.Tags) != 1 || dc.Tags[0].Name != "inline" {
			t.Fatalf("tags = %+v, want [inline]", dc.Tags)
		}
	})

	t.Run("empty everything stays empty", func(t *testing.T) {
		dc := &DriverConfig{Name: "d"}
		if err := MergeFileAndInlineTags(dc, nil); err != nil {
			t.Fatalf("merge: %v", err)
		}
		if len(dc.Tags) != 0 {
			t.Fatalf("tags = %+v, want empty", dc.Tags)
		}
	})
}

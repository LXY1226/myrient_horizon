package myrienttree

import "testing"

func TestMarshalNodeRoundTripIncludesEmptyDirectory(t *testing.T) {
	root := &SerializedNode{
		Name: "/",
		Children: []*SerializedNode{
			{Name: "root.bin", Size: 42},
			{
				Name: "empty",
				Size: EmptyDirectorySize,
			},
			{
				Name: "nested",
				Children: []*SerializedNode{
					{Name: "child.bin", Size: 7},
				},
			},
		},
	}

	data, err := MarshalNode(root)
	if err != nil {
		t.Fatalf("MarshalNode() error = %v", err)
	}

	tree, err := LoadFromBytes[struct{}, struct{}](data)
	if err != nil {
		t.Fatalf("LoadFromBytes() error = %v", err)
	}

	if len(tree.Dirs) != 3 {
		t.Fatalf("len(Dirs) = %d, want 3", len(tree.Dirs))
	}
	if len(tree.Files) != 2 {
		t.Fatalf("len(Files) = %d, want 2", len(tree.Files))
	}
	if _, ok := tree.DirByPath("/empty/"); !ok {
		t.Fatalf("DirByPath(/empty/) = missing, want present")
	}
	if _, ok := tree.DirByPath("/nested/"); !ok {
		t.Fatalf("DirByPath(/nested/) = missing, want present")
	}
	if tree.Files[0].Name != "root.bin" || tree.Files[0].Size != 42 {
		t.Fatalf("root file = %+v, want root.bin size 42", tree.Files[0])
	}
	if tree.Files[1].Name != "child.bin" || tree.Files[1].Size != 7 {
		t.Fatalf("nested file = %+v, want child.bin size 7", tree.Files[1])
	}
}

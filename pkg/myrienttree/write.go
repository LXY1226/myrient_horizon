package myrienttree

import (
	"fmt"
	"os"

	flatbuffers "github.com/google/flatbuffers/go"

	fb "myrient-horizon/pkg/myrienttree/MyrientTree"
)

// EmptyDirectorySize marks a directory in the flatbuffer.
// Files always have a non-negative size. Directories always use this sentinel,
// regardless of whether they currently have children.
const EmptyDirectorySize int64 = -1

// SerializedNode is a minimal tree node used for flatbuffer encoding.
type SerializedNode struct {
	Name     string
	Size     int64
	Children []*SerializedNode
}

// MarshalNode serializes a tree rooted at root into the on-disk flatbuffer
// format used by server and worker.
func MarshalNode(root *SerializedNode) ([]byte, error) {
	if root == nil {
		return nil, fmt.Errorf("marshal node: nil root")
	}

	builder := flatbuffers.NewBuilder(1024)
	rootOff, err := marshalSerializedNode(builder, root)
	if err != nil {
		return nil, err
	}
	fb.FinishNodeBuffer(builder, rootOff)
	out := make([]byte, len(builder.FinishedBytes()))
	copy(out, builder.FinishedBytes())
	return out, nil
}

// WriteNodeFile serializes root and writes it to path.
func WriteNodeFile(path string, root *SerializedNode) error {
	data, err := MarshalNode(root)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write flatbuffer: %w", err)
	}
	return nil
}

func marshalSerializedNode(builder *flatbuffers.Builder, node *SerializedNode) (flatbuffers.UOffsetT, error) {
	if node == nil {
		return 0, fmt.Errorf("marshal node: nil child")
	}

	nameOff := builder.CreateString(node.Name)

	var childrenOff flatbuffers.UOffsetT
	if len(node.Children) > 0 {
		children := make([]flatbuffers.UOffsetT, len(node.Children))
		for i := len(node.Children) - 1; i >= 0; i-- {
			childOff, err := marshalSerializedNode(builder, node.Children[i])
			if err != nil {
				return 0, err
			}
			children[i] = childOff
		}
		fb.NodeStartChildrenVector(builder, len(children))
		for i := len(children) - 1; i >= 0; i-- {
			builder.PrependUOffsetT(children[i])
		}
		childrenOff = builder.EndVector(len(children))
	}

	size := node.Size
	if len(node.Children) > 0 || size < 0 {
		size = EmptyDirectorySize
	}

	fb.NodeStart(builder)
	fb.NodeAddName(builder, nameOff)
	fb.NodeAddSize(builder, size)
	if len(node.Children) > 0 {
		fb.NodeAddChildren(builder, childrenOff)
	}
	return fb.NodeEnd(builder), nil
}

package pqproto

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestMigrationReadTokenPrecedesRequest(t *testing.T) {
	msg := &MigrationStreamingReadClientMessage{
		Token:   []byte("token"),
		Request: &MigrationStreamingReadClientMessage_Read{Read: &Read{}},
	}
	wire, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	field, _, n := protowire.ConsumeTag(wire)
	if n < 0 {
		t.Fatalf("invalid protobuf wire data: %d", n)
	}
	if field != 20 {
		t.Fatalf("first wire field = %d, want token field 20", field)
	}
}

func TestMigrationReadControlWireFields(t *testing.T) {
	tests := []struct {
		message protoreflect.MessageDescriptor
		fields  map[protoreflect.Name]protoreflect.FieldNumber
	}{
		{(&StartRead{}).ProtoReflect().Descriptor(), map[protoreflect.Name]protoreflect.FieldNumber{
			"assign_id": 5, "read_offset": 6, "commit_offset": 7, "verify_read_offset": 8,
		}},
		{(&Assigned{}).ProtoReflect().Descriptor(), map[protoreflect.Name]protoreflect.FieldNumber{
			"assign_id": 5, "read_offset": 6, "end_offset": 7,
		}},
		{(&Release{}).ProtoReflect().Descriptor(), map[protoreflect.Name]protoreflect.FieldNumber{
			"assign_id": 5, "forceful_release": 6, "commit_offset": 7,
		}},
		{(&PartitionStatus{}).ProtoReflect().Descriptor(), map[protoreflect.Name]protoreflect.FieldNumber{
			"assign_id": 5, "committed_offset": 6, "end_offset": 7, "write_watermark_ms": 8,
		}},
	}

	for _, tt := range tests {
		for name, want := range tt.fields {
			field := tt.message.Fields().ByName(name)
			if field == nil {
				t.Fatalf("%s: field %s is missing", tt.message.FullName(), name)
			}
			if got := field.Number(); got != want {
				t.Errorf("%s.%s wire number = %d, want %d", tt.message.FullName(), name, got, want)
			}
		}
	}
}

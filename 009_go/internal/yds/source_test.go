package yds

import (
	"testing"

	"transferia2-go/internal/config"
)

func TestParseEndpoint(t *testing.T) {
	for _, tc := range []struct {
		in, target string
		secure     bool
	}{
		{"grpc://localhost:2135/Root", "localhost:2135", false},
		{"grpcs://lb.example:2135/Root", "lb.example:2135", true},
		{"localhost:2135", "localhost:2135", false},
	} {
		target, secure, err := parseEndpoint(tc.in)
		if err != nil || target != tc.target || secure != tc.secure {
			t.Fatalf("%q => %q %v %v", tc.in, target, secure, err)
		}
	}
}

func TestPartitionZeroUsesPositiveGroupID(t *testing.T) {
	msg := initMessage(config.PQv1Config{TopicPath: "/topic", ConsumerName: "consumer"}, "token", 0)
	settings := msg.GetInitRequest().GetTopicsReadSettings()[0]
	if len(settings.PartitionGroupIds) != 1 || settings.PartitionGroupIds[0] != 1 {
		t.Fatalf("partition_group_ids = %v, want [1] for partition 0", settings.PartitionGroupIds)
	}
}

//go:build tools

package tools

//go:generate protoc -I proto --go_out=. --go_opt=module=transferia2-go --go-grpc_out=. --go-grpc_opt=module=transferia2-go proto/persqueue.proto proto/discovery.proto
